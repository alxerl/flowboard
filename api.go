package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

type App struct {
	store         *Store
	webhookSecret string
	demoMode      bool
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"demo_mode": a.demoMode})
	})
	mux.HandleFunc("POST /api/auth/register", a.register)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/demo", a.demoLogin)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/me", a.withAuth(a.me))
	mux.HandleFunc("GET /api/projects", a.withAuth(a.listProjects))
	mux.HandleFunc("POST /api/projects", a.withAuth(a.createProject))
	mux.HandleFunc("GET /api/projects/{id}/tasks", a.withAuth(a.listTasks))
	mux.HandleFunc("POST /api/projects/{id}/tasks", a.withAuth(a.createTask))
	mux.HandleFunc("GET /api/projects/{id}/metrics", a.withAuth(a.metrics))
	mux.HandleFunc("GET /api/projects/{id}/members", a.withAuth(a.listMembers))
	mux.HandleFunc("POST /api/projects/{id}/members", a.withAuth(a.addMember))
	mux.HandleFunc("GET /api/tasks/{id}", a.withAuth(a.getTask))
	mux.HandleFunc("PATCH /api/tasks/{id}", a.withAuth(a.updateTask))
	mux.HandleFunc("DELETE /api/tasks/{id}", a.withAuth(a.deleteTask))
	mux.HandleFunc("GET /api/tasks/{id}/events", a.withAuth(a.events))
	mux.HandleFunc("POST /webhooks/github", a.githubWebhook)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func dbError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		apiError(w, http.StatusConflict, "key already exists")
		return
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		apiError(w, http.StatusNotFound, "project not found")
		return
	}
	apiError(w, http.StatusInternalServerError, "database error")
}

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("body must contain one JSON object")
	}
	return nil
}

func pathID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (a *App) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := a.store.ProjectsForUser(r.Context(), currentUser(r).ID)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (a *App) createProject(w http.ResponseWriter, r *http.Request) {
	var p Project
	if err := decodeJSON(r, &p); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Key = strings.ToUpper(strings.TrimSpace(p.Key))
	p.Repo = strings.TrimSpace(p.Repo)
	if len(p.Name) < 2 || len(p.Name) > 100 || len(p.Key) < 2 || len(p.Key) > 12 || !validKey(p.Key) || (p.Repo != "" && !validRepo(p.Repo)) {
		apiError(w, 400, "name, key or repository is invalid")
		return
	}
	if err := a.store.CreateProjectForUser(r.Context(), &p, currentUser(r).ID); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func validKey(s string) bool {
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func validRepo(s string) bool {
	parts := strings.Split(s, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(s, " \t\n")
}
func validStatus(s string) bool {
	return s == "backlog" || s == "in_progress" || s == "review" || s == "done"
}
func validPriority(s string) bool { return s == "low" || s == "medium" || s == "high" }

func (a *App) listTasks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid project id")
		return
	}
	if _, ok := a.projectRole(w, r, id); !ok {
		return
	}
	tasks, err := a.store.Tasks(r.Context(), id)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (a *App) createTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid project id")
		return
	}
	if _, ok := a.projectRole(w, r, id); !ok {
		return
	}
	var t Task
	if err := decodeJSON(r, &t); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	t.ProjectID = id
	t.Title = strings.TrimSpace(t.Title)
	if t.Status == "" {
		t.Status = "backlog"
	}
	if t.Priority == "" {
		t.Priority = "medium"
	}
	if len(t.Title) < 2 || len(t.Title) > 200 || len(t.Description) > 4000 || len(t.Assignee) > 100 || !validStatus(t.Status) || !validPriority(t.Priority) {
		apiError(w, 400, "invalid task fields")
		return
	}
	if err := a.store.CreateTask(r.Context(), &t); err != nil {
		dbError(w, err)
		return
	}
	_ = a.store.AddEvent(r.Context(), t.ID, "created", "Task created")
	writeJSON(w, http.StatusCreated, t)
}

func (a *App) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid task id")
		return
	}
	t, ok := a.taskAccess(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type taskPatch struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	Priority    *string `json:"priority"`
	Assignee    *string `json:"assignee"`
}

func (a *App) updateTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid task id")
		return
	}
	var patch taskPatch
	if err := decodeJSON(r, &patch); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	t, ok := a.taskAccess(w, r, id)
	if !ok {
		return
	}
	oldStatus := t.Status
	if patch.Title != nil {
		t.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Description != nil {
		t.Description = *patch.Description
	}
	if patch.Status != nil {
		t.Status = *patch.Status
	}
	if patch.Priority != nil {
		t.Priority = *patch.Priority
	}
	if patch.Assignee != nil {
		t.Assignee = *patch.Assignee
	}
	if len(t.Title) < 2 || len(t.Title) > 200 || len(t.Description) > 4000 || len(t.Assignee) > 100 || !validStatus(t.Status) || !validPriority(t.Priority) {
		apiError(w, 400, "invalid task fields")
		return
	}
	if err := a.store.UpdateTask(r.Context(), &t); err != nil {
		dbError(w, err)
		return
	}
	if oldStatus != t.Status {
		_ = a.store.AddEvent(r.Context(), t.ID, "status", oldStatus+" → "+t.Status)
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *App) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid task id")
		return
	}
	if _, ok := a.taskAccess(w, r, id); !ok {
		return
	}
	if err := a.store.DeleteTask(r.Context(), id); err != nil {
		dbError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) events(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid task id")
		return
	}
	if _, ok := a.taskAccess(w, r, id); !ok {
		return
	}
	events, err := a.store.Events(r.Context(), id)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid project id")
		return
	}
	if _, ok := a.projectRole(w, r, id); !ok {
		return
	}
	m, err := a.store.Metrics(r.Context(), id)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *App) listMembers(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid project id")
		return
	}
	if _, ok := a.projectRole(w, r, id); !ok {
		return
	}
	members, err := a.store.Members(r.Context(), id)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, members)
}

func (a *App) addMember(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid project id")
		return
	}
	role, ok := a.projectRole(w, r, id)
	if !ok {
		return
	}
	if role != "owner" {
		apiError(w, 403, "only project owners can add members")
		return
	}
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Role != "member" && input.Role != "owner" {
		apiError(w, 400, "invalid role")
		return
	}
	if err := a.store.AddMember(r.Context(), id, input.Email, input.Role); err != nil {
		dbError(w, err)
		return
	}
	w.WriteHeader(204)
}
