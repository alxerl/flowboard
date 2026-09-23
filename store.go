package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ db *pgxpool.Pool }

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS projects (
 id BIGSERIAL PRIMARY KEY,
 name TEXT NOT NULL,
 key TEXT NOT NULL UNIQUE,
 repo TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS projects_repo_idx ON projects(lower(repo)) WHERE repo<>'';
CREATE TABLE IF NOT EXISTS users (
 id BIGSERIAL PRIMARY KEY,
 email TEXT NOT NULL UNIQUE,
 display_name TEXT NOT NULL,
 password_hash TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS project_members (
 project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK (role IN ('owner','member')),
 PRIMARY KEY(project_id,user_id)
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
 id BIGSERIAL PRIMARY KEY,
 project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 title TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in_progress','review','done')),
 priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low','medium','high')),
 assignee TEXT NOT NULL DEFAULT '',
 pr_number INTEGER,
 pr_url TEXT NOT NULL DEFAULT '',
 issue_number INTEGER,
 issue_url TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 status_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 done_at TIMESTAMPTZ
);
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS issue_number INTEGER;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS issue_url TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS status_changed_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE UNIQUE INDEX IF NOT EXISTS tasks_issue_idx ON tasks(project_id,issue_number) WHERE issue_number IS NOT NULL;
CREATE INDEX IF NOT EXISTS tasks_project_status_idx ON tasks(project_id, status);
CREATE TABLE IF NOT EXISTS task_events (
 id BIGSERIAL PRIMARY KEY,
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 message TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS task_events_task_idx ON task_events(task_id, created_at DESC);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
 delivery_id TEXT PRIMARY KEY,
 received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

func (s *Store) Migrate(ctx context.Context) error {
	// The original prototype stored tasks in a table without project_id.
	_, err := s.db.Exec(ctx, `DO $$ BEGIN
  IF to_regclass('public.tasks') IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='tasks' AND column_name='project_id'
	) THEN
	  CREATE TABLE IF NOT EXISTS legacy_tasks AS TABLE tasks;
	  DROP TABLE tasks CASCADE;
	END IF;
END $$;`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, schema)
	if err != nil {
		return err
	}
	var legacy bool
	if err := s.db.QueryRow(ctx, `SELECT to_regclass('public.legacy_tasks') IS NOT NULL`).Scan(&legacy); err != nil {
		return err
	}
	if legacy {
		_, err = s.db.Exec(ctx, `INSERT INTO projects(name,key) VALUES('Imported tasks','LEGACY') ON CONFLICT (key) DO NOTHING;
INSERT INTO tasks(id,project_id,title,status)
SELECT id,(SELECT id FROM projects WHERE key='LEGACY'),COALESCE(NULLIF(name,''),'Untitled task'),
CASE WHEN lower(status) IN ('done','complete','completed') THEN 'done' ELSE 'backlog' END
FROM legacy_tasks ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('tasks','id'),GREATEST((SELECT COALESCE(MAX(id),1) FROM tasks),1));
DROP TABLE legacy_tasks;`)
	}
	return err
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,key,repo,created_at FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Repo, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) ProjectsForUser(ctx context.Context, userID int64) ([]Project, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id,p.name,p.key,p.repo,p.created_at FROM projects p JOIN project_members m ON m.project_id=p.id WHERE m.user_id=$1 ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Repo, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) CreateProjectForUser(ctx context.Context, p *Project, userID int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `INSERT INTO projects(name,key,repo) VALUES($1,$2,$3) RETURNING id,created_at`, p.Name, p.Key, p.Repo).Scan(&p.ID, &p.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`, p.ID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MemberRole(ctx context.Context, projectID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

func (s *Store) AddMember(ctx context.Context, projectID int64, email, role string) error {
	cmd, err := s.db.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) SELECT $1,id,$3 FROM users WHERE email=$2 ON CONFLICT(project_id,user_id) DO UPDATE SET role=EXCLUDED.role`, projectID, email, role)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Members(ctx context.Context, projectID int64) ([]Member, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id,u.email,u.display_name,m.role FROM users u JOIN project_members m ON m.user_id=u.id WHERE m.project_id=$1 ORDER BY m.role DESC,u.display_name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Email, &m.DisplayName, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	return s.db.QueryRow(ctx, `INSERT INTO projects(name,key,repo) VALUES($1,$2,$3) RETURNING id,created_at`, p.Name, p.Key, p.Repo).Scan(&p.ID, &p.CreatedAt)
}

const taskColumns = `id,project_id,title,description,status,priority,assignee,pr_number,pr_url,issue_number,issue_url,created_at,updated_at`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.Assignee, &t.PRNumber, &t.PRURL, &t.IssueNumber, &t.IssueURL, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) Tasks(ctx context.Context, projectID int64) ([]Task, error) {
	rows, err := s.db.Query(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) Task(ctx context.Context, id int64) (Task, error) {
	t, err := scanTask(s.db.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	return s.db.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,priority,assignee,done_at) VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $4='done' THEN now() ELSE NULL END) RETURNING id,created_at,updated_at`, t.ProjectID, t.Title, t.Description, t.Status, t.Priority, t.Assignee).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (s *Store) UpdateTask(ctx context.Context, t *Task) error {
	row := s.db.QueryRow(ctx, `UPDATE tasks SET title=$2,description=$3,status=$4,priority=$5,assignee=$6,updated_at=now(),status_changed_at=CASE WHEN status<>$4 THEN now() ELSE status_changed_at END,done_at=CASE WHEN $4='done' THEN COALESCE(done_at,now()) ELSE NULL END WHERE id=$1 RETURNING updated_at`, t.ID, t.Title, t.Description, t.Status, t.Priority, t.Assignee)
	if err := row.Scan(&t.UpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	cmd, err := s.db.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddEvent(ctx context.Context, id int64, kind, message string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO task_events(task_id,kind,message) VALUES($1,$2,$3)`, id, kind, message)
	return err
}

func (s *Store) Events(ctx context.Context, id int64) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT id,task_id,kind,message,created_at FROM task_events WHERE task_id=$1 ORDER BY created_at DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) Metrics(ctx context.Context, projectID int64) (Metrics, error) {
	var m Metrics
	err := s.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='backlog'),count(*) FILTER (WHERE status='in_progress'),count(*) FILTER (WHERE status='review'),count(*) FILTER (WHERE status='done'),COALESCE(round((avg(extract(epoch FROM (done_at-created_at))/86400) FILTER (WHERE done_at IS NOT NULL))::numeric,1),0)::double precision FROM tasks WHERE project_id=$1`, projectID).Scan(&m.Total, &m.Backlog, &m.InProgress, &m.Review, &m.Done, &m.AvgDays)
	if m.Total > 0 {
		m.Completion = float64(m.Done) * 100 / float64(m.Total)
	}
	return m, err
}

func (s *Store) Insights(ctx context.Context, projectID int64) (Insights, error) {
	result := Insights{Daily: []DailyCompletion{}, Bottlenecks: []Bottleneck{}}
	rows, err := s.db.Query(ctx, `SELECT day::date,count(t.id)::int FROM generate_series(current_date-13,current_date,interval '1 day') AS day LEFT JOIN tasks t ON t.project_id=$1 AND t.done_at::date=day::date GROUP BY day ORDER BY day`, projectID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var day time.Time
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			rows.Close()
			return result, err
		}
		result.Daily = append(result.Daily, DailyCompletion{Day: day.Format("2006-01-02"), Count: count})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT id,title,status,round((extract(epoch FROM (now()-status_changed_at))/86400)::numeric,1)::double precision FROM tasks WHERE project_id=$1 AND ((status='review' AND status_changed_at<now()-interval '2 days') OR (status='in_progress' AND status_changed_at<now()-interval '5 days')) ORDER BY status_changed_at LIMIT 5`, projectID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var b Bottleneck
		if err := rows.Scan(&b.TaskID, &b.Title, &b.Status, &b.Days); err != nil {
			return result, err
		}
		result.Bottlenecks = append(result.Bottlenecks, b)
	}
	return result, rows.Err()
}

func (s *Store) ApplyPullRequest(ctx context.Context, delivery, repo string, number int, url, title, body, action string, merged bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries(delivery_id) VALUES($1) ON CONFLICT DO NOTHING`, delivery)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return nil
	}
	var projectID int64
	var projectKey string
	if err := tx.QueryRow(ctx, `SELECT id,key FROM projects WHERE lower(repo)=lower($1)`, repo).Scan(&projectID, &projectKey); errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	} else if err != nil {
		return err
	}
	ids := taskIDs(projectKey, title+" "+body)
	for _, id := range ids {
		var currentStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1 AND project_id=$2`, id, projectID).Scan(&currentStatus); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		status := currentStatus
		switch {
		case action == "opened" || action == "reopened" || action == "ready_for_review":
			status = "review"
		case action == "closed" && merged:
			status = "done"
		case action == "closed" && !merged:
			status = "in_progress"
		}
		_, err = tx.Exec(ctx, `UPDATE tasks SET status_changed_at=CASE WHEN status<>$2 THEN now() ELSE status_changed_at END,status=$2,pr_number=$3,pr_url=$4,updated_at=now(),done_at=CASE WHEN $2='done' THEN COALESCE(done_at,now()) ELSE NULL END WHERE id=$1`, id, status, number, url)
		if err != nil {
			return err
		}
		message := fmt.Sprintf("PR #%d %s", number, strings.ReplaceAll(action, "_", " "))
		if merged {
			message = fmt.Sprintf("PR #%d merged", number)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO task_events(task_id,kind,message) VALUES($1,'github',$2)`, id, message); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ApplyIssue(ctx context.Context, delivery, repo string, number int, url, title, body, assignee, action string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries(delivery_id) VALUES($1) ON CONFLICT DO NOTHING`, delivery)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return nil
	}
	var projectID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE lower(repo)=lower($1)`, repo).Scan(&projectID); errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	} else if err != nil {
		return err
	}
	title = limitText(strings.TrimSpace(title), 200)
	if title == "" {
		title = "Untitled GitHub issue"
	}
	body = limitText(body, 4000)
	var id int64
	var oldStatus string
	err = tx.QueryRow(ctx, `SELECT id,status FROM tasks WHERE project_id=$1 AND issue_number=$2`, projectID, number).Scan(&id, &oldStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		status := "backlog"
		if action == "closed" {
			status = "done"
		}
		err = tx.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,assignee,issue_number,issue_url,done_at) VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $4='done' THEN now() ELSE NULL END) RETURNING id`, projectID, title, body, status, assignee, number, url).Scan(&id)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		status := oldStatus
		if action == "closed" {
			status = "done"
		}
		if action == "reopened" {
			status = "backlog"
		}
		_, err = tx.Exec(ctx, `UPDATE tasks SET title=$2,description=$3,assignee=$4,issue_url=$5,status_changed_at=CASE WHEN status<>$6 THEN now() ELSE status_changed_at END,status=$6,updated_at=now(),done_at=CASE WHEN $6='done' THEN COALESCE(done_at,now()) ELSE NULL END WHERE id=$1`, id, title, body, assignee, url, status)
		if err != nil {
			return err
		}
	}
	message := fmt.Sprintf("GitHub issue #%d %s", number, action)
	if _, err = tx.Exec(ctx, `INSERT INTO task_events(task_id,kind,message) VALUES($1,'github',$2)`, id, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func limitText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Store) SeedDemo(ctx context.Context) error {
	u, err := s.UserByEmail(ctx, "demo@flowboard.local")
	if errors.Is(err, ErrNotFound) {
		password := make([]byte, 32)
		if _, err = rand.Read(password); err != nil {
			return err
		}
		u, err = s.RegisterUser(ctx, "demo@flowboard.local", "Demo workspace", hex.EncodeToString(password))
	}
	if err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM projects`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	p := Project{Name: "Atlas release", Key: "ATL", Repo: "alxerl/flowboard"}
	if err := s.CreateProject(ctx, &p); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`, p.ID, u.ID); err != nil {
		return err
	}
	items := []Task{
		{Title: "Ship the public dashboard", Description: "Show delivery metrics and recent activity.", Status: "in_progress", Priority: "high", Assignee: "Alex"},
		{Title: "Connect GitHub pull requests", Description: "Move tasks automatically when a PR is opened or merged.", Status: "review", Priority: "high", Assignee: "Alex"},
		{Title: "Add workspace invitations", Description: "Invite teammates into a project.", Status: "backlog", Priority: "medium", Assignee: "Maria"},
		{Title: "Design the release board", Description: "Create a visual workflow for the team.", Status: "done", Priority: "medium", Assignee: "Maria"},
		{Title: "Define the API contract", Description: "Agree on resources and task transitions.", Status: "done", Priority: "high", Assignee: "Alex"},
		{Title: "Set up project database", Description: "Create the initial PostgreSQL schema.", Status: "done", Priority: "medium", Assignee: "Maria"},
	}
	offsets := []int{6, 3, 1, 1, 3, 5}
	for i, item := range items {
		item.ProjectID = p.ID
		if err := s.CreateTask(ctx, &item); err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `UPDATE tasks SET created_at=now()-(($2+4)*interval '1 day'),status_changed_at=now()-($2*interval '1 day'),done_at=CASE WHEN status='done' THEN now()-($2*interval '1 day') ELSE NULL END WHERE id=$1`, item.ID, offsets[i]); err != nil {
			return err
		}
		if err := s.AddEvent(ctx, item.ID, "created", "Task created"); err != nil {
			return err
		}
	}
	return nil
}
