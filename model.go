package main

import "time"

type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	Repo      string    `json:"repo"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type Member struct {
	User
	Role string `json:"role"`
}

type Task struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	Assignee    string    `json:"assignee"`
	PRNumber    *int      `json:"pr_number,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	IssueNumber *int      `json:"issue_number,omitempty"`
	IssueURL    string    `json:"issue_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type Metrics struct {
	Total      int     `json:"total"`
	Backlog    int     `json:"backlog"`
	InProgress int     `json:"in_progress"`
	Review     int     `json:"review"`
	Done       int     `json:"done"`
	Completion float64 `json:"completion"`
	AvgDays    float64 `json:"avg_cycle_days"`
}
