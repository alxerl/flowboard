package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeliveryFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS webhook_deliveries,task_events,tasks,projects,legacy_tasks,project_members,sessions,users CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE tasks(id SERIAL PRIMARY KEY,name TEXT,status TEXT); INSERT INTO tasks(name,status) VALUES('Old task','done')`); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: pool}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Key != "LEGACY" {
		t.Fatalf("legacy migration: %v, %v", projects, err)
	}
	owner, err := s.RegisterUser(ctx, "owner@example.com", "Project owner", "strong-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateUser(ctx, owner.Email, "strong-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateUser(ctx, owner.Email, "wrong-password"); err != ErrNotFound {
		t.Fatalf("wrong password accepted: %v", err)
	}
	p := Project{Name: "Release", Key: "REL", Repo: "example/flowboard"}
	if err := s.CreateProjectForUser(ctx, &p, owner.ID); err != nil {
		t.Fatal(err)
	}
	member, err := s.RegisterUser(ctx, "member@example.com", "Team member", "another-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MemberRole(ctx, p.ID, member.ID); err != ErrNotFound {
		t.Fatalf("unexpected access before invitation: %v", err)
	}
	if err := s.AddMember(ctx, p.ID, member.Email, "member"); err != nil {
		t.Fatal(err)
	}
	if role, err := s.MemberRole(ctx, p.ID, member.ID); err != nil || role != "member" {
		t.Fatalf("member role: %s, %v", role, err)
	}
	token, err := s.NewSession(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sessionUser, err := s.SessionUser(ctx, token); err != nil || sessionUser.ID != member.ID {
		t.Fatalf("session: %+v, %v", sessionUser, err)
	}
	task := Task{ProjectID: p.ID, Title: "Ship integration", Status: "in_progress", Priority: "high"}
	if err := s.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	prTitle := fmt.Sprintf("REL-%d Ship integration", task.ID)
	if err := s.ApplyPullRequest(ctx, "delivery-1", p.Repo, 42, "https://github.com/example/flowboard/pull/42", prTitle, "", "opened", false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil || got.Status != "review" || got.PRNumber == nil || *got.PRNumber != 42 {
		t.Fatalf("opened PR: %+v, %v", got, err)
	}
	if err := s.ApplyPullRequest(ctx, "delivery-1", p.Repo, 42, got.PRURL, prTitle, "", "opened", false); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPullRequest(ctx, "delivery-2", p.Repo, 42, got.PRURL, prTitle, "", "closed", true); err != nil {
		t.Fatal(err)
	}
	got, err = s.Task(ctx, task.ID)
	if err != nil || got.Status != "done" {
		t.Fatalf("merged PR: %+v, %v", got, err)
	}
	m, err := s.Metrics(ctx, p.ID)
	if err != nil || m.Total != 1 || m.Done != 1 || m.Completion != 100 {
		t.Fatalf("metrics: %+v, %v", m, err)
	}
	events, err := s.Events(ctx, task.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v, %v", events, err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS webhook_deliveries,task_events,tasks,projects,legacy_tasks,project_members,sessions,users CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	demo, err := s.UserByEmail(ctx, "demo@flowboard.local")
	if err != nil {
		t.Fatal(err)
	}
	demoProjects, err := s.ProjectsForUser(ctx, demo.ID)
	if err != nil || len(demoProjects) != 1 || demoProjects[0].Key != "ATL" {
		t.Fatalf("demo workspace: %v, %v", demoProjects, err)
	}
}
