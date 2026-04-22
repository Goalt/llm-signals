package xapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

func TestGetJSONFeedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer token123" {
			t.Fatalf("expected bearer auth header, got %q", auth)
		}

		switch r.URL.Path {
		case "/users/by/username/test_user":
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"test_user","description":"x profile"}}`))
		case "/users/42/tweets":
			_, _ = w.Write([]byte(`{"data":[{"id":"100","text":"hello & world","created_at":"2026-04-21T12:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.Now = func() time.Time { return time.Date(2026, 4, 21, 15, 0, 0, 0, time.UTC) }

	raw, err := svc.GetJSONFeed("test_user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}

	if feed.Link != "https://x.com/test_user" {
		t.Fatalf("unexpected feed link: %q", feed.Link)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(feed.Items))
	}
	if feed.Items[0].ID != "100" || feed.Items[0].Link != "https://x.com/test_user/status/100" {
		t.Fatalf("unexpected item identity: %+v", feed.Items[0])
	}
	if !strings.Contains(feed.Items[0].Description, "hello &amp; world") {
		t.Fatalf("expected escaped text in description, got %q", feed.Items[0].Description)
	}
}

func TestGetJSONFeedValidation(t *testing.T) {
	svc := NewService("", http.DefaultClient)
	if _, err := svc.GetJSONFeed("gooduser"); err == nil {
		t.Fatalf("expected token validation error")
	}

	svc = NewService("token123", http.DefaultClient)
	if _, err := svc.GetJSONFeed("bad-user"); err == nil {
		t.Fatalf("expected username validation error")
	}
}

func TestGetJSONFeedFilteredStream(t *testing.T) {
	var rulesAdded int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer token123" {
			t.Fatalf("expected bearer auth header, got %q", auth)
		}

		switch {
		case r.URL.Path == "/users/by/username/test_user":
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"test_user","description":"x profile"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			if !strings.Contains(readBody(t, r), `"value":"from:test_user"`) {
				t.Fatalf("expected from:test_user rule in add request")
			}
			rulesAdded++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1","value":"from:test_user"}]}`))
		case r.URL.Path == "/tweets/search/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello & world\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"test_user\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true
	svc.Now = func() time.Time { return time.Date(2026, 4, 21, 15, 0, 0, 0, time.UTC) }

	raw, err := svc.GetJSONFeed("test_user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rulesAdded != 1 {
		t.Fatalf("expected one rules add request, got %d", rulesAdded)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(feed.Items))
	}
	if feed.Items[0].ID != "100" || feed.Items[0].Link != "https://x.com/test_user/status/100" {
		t.Fatalf("unexpected item identity: %+v", feed.Items[0])
	}
}

func TestGetJSONFeedFilteredStreamExistingRule(t *testing.T) {
	var postRulesCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/by/username/test_user":
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"test_user","description":"x profile"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1","value":"from:test_user"}]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			postRulesCalled = true
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/tweets/search/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"test_user\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true

	if _, err := svc.GetJSONFeed("test_user"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postRulesCalled {
		t.Fatalf("did not expect rule create call when rule already exists")
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
