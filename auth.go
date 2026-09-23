package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type userContextKey struct{}

func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) RegisterUser(ctx context.Context, email, name, password string) (User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	u := User{Email: email, DisplayName: name}
	if err := tx.QueryRow(ctx, `INSERT INTO users(email,display_name,password_hash) VALUES($1,$2,$3) RETURNING id`, email, name, string(hash)).Scan(&u.ID); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) SELECT p.id,$1,'owner' FROM projects p WHERE NOT EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=p.id)`, u.ID); err != nil {
		return User{}, err
	}
	return u, tx.Commit(ctx)
}

func (s *Store) AuthenticateUser(ctx context.Context, email, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx, `SELECT id,email,display_name,password_hash FROM users WHERE email=$1`, email).Scan(&u.ID, &u.Email, &u.DisplayName, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `SELECT id,email,display_name FROM users WHERE email=$1`, email).Scan(&u.ID, &u.Email, &u.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) NewSession(ctx context.Context, userID int64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	_, err := s.db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, sessionHash(token), userID, time.Now().Add(7*24*time.Hour))
	return token, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `SELECT u.id,u.email,u.display_name FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, sessionHash(token)).Scan(&u.ID, &u.Email, &u.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, sessionHash(token))
	return err
}

func (a *App) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("flowboard_session")
		if err != nil {
			apiError(w, 401, "sign in required")
			return
		}
		u, err := a.store.SessionUser(r.Context(), cookie.Value)
		if err != nil {
			apiError(w, 401, "sign in required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, u)))
	}
}

func currentUser(r *http.Request) User { return r.Context().Value(userContextKey{}).(User) }

func (a *App) projectRole(w http.ResponseWriter, r *http.Request, projectID int64) (string, bool) {
	role, err := a.store.MemberRole(r.Context(), projectID, currentUser(r).ID)
	if errors.Is(err, ErrNotFound) {
		apiError(w, 404, "project not found")
		return "", false
	}
	if err != nil {
		dbError(w, err)
		return "", false
	}
	return role, true
}

func (a *App) taskAccess(w http.ResponseWriter, r *http.Request, id int64) (Task, bool) {
	t, err := a.store.Task(r.Context(), id)
	if err != nil {
		dbError(w, err)
		return Task{}, false
	}
	if _, ok := a.projectRole(w, r, t.ProjectID); !ok {
		return Task{}, false
	}
	return t, true
}

func (a *App) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: "flowboard_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"), MaxAge: 7 * 24 * 3600})
}

func (a *App) signIn(w http.ResponseWriter, r *http.Request, u User) {
	token, err := a.store.NewSession(r.Context(), u.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	a.setSessionCookie(w, r, token)
	writeJSON(w, 200, u)
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if len(input.Email) > 254 || !strings.Contains(input.Email, "@") || len(input.DisplayName) < 2 || len(input.DisplayName) > 100 || len(input.Password) < 10 || len(input.Password) > 72 {
		apiError(w, 400, "provide a valid email, name and password of 10–72 characters")
		return
	}
	u, err := a.store.RegisterUser(r.Context(), input.Email, input.DisplayName, input.Password)
	if err != nil {
		dbError(w, err)
		return
	}
	a.signIn(w, r, u)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	u, err := a.store.AuthenticateUser(r.Context(), strings.ToLower(strings.TrimSpace(input.Email)), input.Password)
	if errors.Is(err, ErrNotFound) {
		apiError(w, 401, "invalid email or password")
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	a.signIn(w, r, u)
}

func (a *App) demoLogin(w http.ResponseWriter, r *http.Request) {
	if !a.demoMode {
		apiError(w, 404, "not found")
		return
	}
	u, err := a.store.UserByEmail(r.Context(), "demo@flowboard.local")
	if err != nil {
		dbError(w, err)
		return
	}
	a.signIn(w, r, u)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("flowboard_session"); err == nil {
		_ = a.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "flowboard_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(204)
}

func (a *App) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, currentUser(r)) }
