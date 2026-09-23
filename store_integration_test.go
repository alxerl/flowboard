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
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS webhook_deliveries,task_events,tasks,projects,legacy_tasks CASCADE`); err != nil {
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
	p := Project{Name: "Release", Key: "REL", Repo: "example/flowboard"}
	if err := s.CreateProject(ctx, &p); err != nil {
		t.Fatal(err)
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
}
