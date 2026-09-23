package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTaskIDsUseProjectKeyAndDeduplicate(t *testing.T) {
	got := taskIDs("ATL", "Fix ATL-12, review atl-12 and ignore OTHER-7. Then ATL-34.")
	if !reflect.DeepEqual(got, []int64{12, 34}) {
		t.Fatalf("taskIDs = %v", got)
	}
}

func TestWebhookRejectsUnsignedPayload(t *testing.T) {
	app := &App{webhookSecret: "secret"}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`{"action":"opened"}`))
	w := httptest.NewRecorder()
	app.githubWebhook(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestWebhookSignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !validSignature("secret", body, signature) {
		t.Fatal("valid signature rejected")
	}
	if validSignature("secret", []byte("tampered"), signature) {
		t.Fatal("tampered payload accepted")
	}
}
