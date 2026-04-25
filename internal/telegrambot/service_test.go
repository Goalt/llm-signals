package telegrambot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendAndCheckAccess(t *testing.T) {
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/bottest:token/getMe" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	svc := NewService("test:token", "42", server.Client())
	svc.BaseURL = server.URL
	if err := svc.CheckAccess(context.Background()); err != nil {
		t.Fatalf("check access: %v", err)
	}
	if err := svc.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/bottest:token/sendMessage" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if !strings.Contains(gotBody, `"chat_id":"42"`) || !strings.Contains(gotBody, `"text":"hello"`) {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}

func TestTelegramErrors(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		svc := NewService("", "", http.DefaultClient)
		if err := svc.Send(context.Background(), "x"); err == nil {
			t.Fatalf("expected validation error")
		}
	})

	t.Run("api ok false", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"description":"bad token"}`))
		}))
		defer server.Close()
		svc := NewService("tok", "42", server.Client())
		svc.BaseURL = server.URL
		if err := svc.CheckAccess(context.Background()); err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Fatalf("expected API error, got %v", err)
		}
	})
}
