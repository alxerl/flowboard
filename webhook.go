package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

func taskIDs(key, text string) []int64 {
	taskRef := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(key) + `-(\d+)\b`)
	seen := map[int64]bool{}
	ids := []int64{}
	for _, match := range taskRef.FindAllStringSubmatch(text, -1) {
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err == nil && id > 0 && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func validSignature(secret string, body []byte, header string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type pullRequestEvent struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
	} `json:"pull_request"`
}

func (a *App) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if a.webhookSecret == "" {
		apiError(w, http.StatusServiceUnavailable, "webhook not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		apiError(w, 400, "invalid body")
		return
	}
	if !validSignature(a.webhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		apiError(w, 401, "invalid signature")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		writeJSON(w, 200, map[string]string{"status": "ignored"})
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		apiError(w, 400, "missing delivery id")
		return
	}
	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		apiError(w, 400, "invalid JSON")
		return
	}
	if event.Repository.FullName == "" || event.Number < 1 || event.PullRequest.HTMLURL == "" {
		apiError(w, 400, "invalid pull request event")
		return
	}
	if err := a.store.ApplyPullRequest(r.Context(), delivery, event.Repository.FullName, event.Number, event.PullRequest.HTMLURL, event.PullRequest.Title, event.PullRequest.Body, event.Action, event.PullRequest.Merged); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "processed"})
}
