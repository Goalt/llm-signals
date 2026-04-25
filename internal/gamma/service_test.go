package gamma

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventsByTagBuildsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := NewService(server.Client())
	svc.BaseURL = server.URL

	body, err := svc.EventsByTag(context.Background(), " politics ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != `{"ok":true}` {
		t.Fatalf("unexpected body: %q", body)
	}
	if gotPath != "/events/keyset" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=20") || !strings.Contains(gotQuery, "ascending=true") || !strings.Contains(gotQuery, "tag_slug=politics") {
		t.Fatalf("unexpected query: %q", gotQuery)
	}
	if gotUA != "tg-channel-to-rss" {
		t.Fatalf("unexpected user-agent: %q", gotUA)
	}
}

func TestEventsByTagErrors(t *testing.T) {
	t.Run("bad status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
		defer server.Close()

		svc := NewService(server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.EventsByTag(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "status 502") {
			t.Fatalf("expected status error, got %v", err)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer server.Close()

		svc := NewService(server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.EventsByTag(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "empty body") {
			t.Fatalf("expected empty-body error, got %v", err)
		}
	})

	t.Run("invalid base url", func(t *testing.T) {
		svc := NewService(http.DefaultClient)
		svc.BaseURL = "://bad"
		if _, err := svc.EventsByTag(context.Background(), ""); err == nil {
			t.Fatalf("expected base URL error")
		}
	})
}
