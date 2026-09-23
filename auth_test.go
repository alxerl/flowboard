package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectsRequireSession(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	w := httptest.NewRecorder()
	app.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}
