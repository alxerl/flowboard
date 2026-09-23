package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 done_at TIMESTAMPTZ
);
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

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	return s.db.QueryRow(ctx, `INSERT INTO projects(name,key,repo) VALUES($1,$2,$3) RETURNING id,created_at`, p.Name, p.Key, p.Repo).Scan(&p.ID, &p.CreatedAt)
}

const taskColumns = `id,project_id,title,description,status,priority,assignee,pr_number,pr_url,created_at,updated_at`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.Assignee, &t.PRNumber, &t.PRURL, &t.CreatedAt, &t.UpdatedAt)
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
	return s.db.QueryRow(ctx, `INSERT INTO tasks(project_id,title,description,status,priority,assignee) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at,updated_at`, t.ProjectID, t.Title, t.Description, t.Status, t.Priority, t.Assignee).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (s *Store) UpdateTask(ctx context.Context, t *Task) error {
	row := s.db.QueryRow(ctx, `UPDATE tasks SET title=$2,description=$3,status=$4,priority=$5,assignee=$6,updated_at=now(),done_at=CASE WHEN $4='done' THEN COALESCE(done_at,now()) ELSE NULL END WHERE id=$1 RETURNING updated_at`, t.ID, t.Title, t.Description, t.Status, t.Priority, t.Assignee)
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
		_, err = tx.Exec(ctx, `UPDATE tasks SET status=$2,pr_number=$3,pr_url=$4,updated_at=now(),done_at=CASE WHEN $2='done' THEN COALESCE(done_at,now()) ELSE NULL END WHERE id=$1`, id, status, number, url)
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

func (s *Store) SeedDemo(ctx context.Context) error {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM projects`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	p := Project{Name: "Atlas release", Key: "ATL", Repo: "alxerl/pet"}
	if err := s.CreateProject(ctx, &p); err != nil {
		return err
	}
	items := []Task{
		{Title: "Ship the public dashboard", Description: "Show delivery metrics and recent activity.", Status: "in_progress", Priority: "high", Assignee: "Alex"},
		{Title: "Connect GitHub pull requests", Description: "Move tasks automatically when a PR is opened or merged.", Status: "review", Priority: "high", Assignee: "Alex"},
		{Title: "Add workspace invitations", Description: "Invite teammates into a project.", Status: "backlog", Priority: "medium", Assignee: "Maria"},
		{Title: "Design the release board", Description: "Create a visual workflow for the team.", Status: "done", Priority: "medium", Assignee: "Maria"},
	}
	for _, item := range items {
		item.ProjectID = p.ID
		if err := s.CreateTask(ctx, &item); err != nil {
			return err
		}
		if item.Status == "done" {
			if err := s.UpdateTask(ctx, &item); err != nil {
				return err
			}
		}
		if err := s.AddEvent(ctx, item.ID, "created", "Task created"); err != nil {
			return err
		}
	}
	return nil
}
