package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/xapi"
)

func TestXAPI_GetJSONFeed_Success(t *testing.T) {
	var userHits, tweetHits int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("bad auth: %q", auth)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Errorf("missing user-agent")
		}
		switch {
		case r.URL.Path == "/users/by/username/alice":
			atomic.AddInt32(&userHits, 1)
			_, _ = w.Write([]byte(`{"data":{"id":"u1","name":"Alice","username":"alice","description":"bio"}}`))
		case r.URL.Path == "/users/u1/tweets":
			atomic.AddInt32(&tweetHits, 1)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"t1","text":"<hello>","created_at":"2026-04-21T12:00:00Z"},
				{"id":"t2","text":"second","created_at":"not-a-date"}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := xapi.NewService("tok", server.Client())
	svc.BaseURL = server.URL
	fixed := time.Date(2026, 4, 21, 15, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }

	raw, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&userHits) != 1 || atomic.LoadInt32(&tweetHits) != 1 {
		t.Fatalf("expected one call to each endpoint; user=%d tweets=%d", userHits, tweetHits)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if feed.Title != "@alice" || feed.Link != "https://x.com/alice" || feed.Description != "bio" {
		t.Fatalf("unexpected feed header: %+v", feed)
	}
	if !feed.Created.Equal(fixed) {
		t.Fatalf("feed.Created=%v want %v", feed.Created, fixed)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(feed.Items))
	}

	// First tweet — valid created_at, escaped text.
	it := feed.Items[0]
	if it.ID != "t1" || it.Link != "https://x.com/alice/status/t1" {
		t.Fatalf("item[0] identity: %+v", it)
	}
	if !strings.Contains(it.Description, "&lt;hello&gt;") {
		t.Fatalf("expected escaped text, got %q", it.Description)
	}
	wantTime := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	if !it.Created.Equal(wantTime) {
		t.Fatalf("item[0].Created=%v want %v", it.Created, wantTime)
	}

	// Second tweet — bad created_at falls back to svc.Now().
	if !feed.Items[1].Created.Equal(fixed) {
		t.Fatalf("item[1].Created=%v want %v (fallback)", feed.Items[1].Created, fixed)
	}
}

func TestXAPI_GetJSONFeed_Validation(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		svc := xapi.NewService("   ", http.DefaultClient)
		if _, err := svc.GetJSONFeed("alice"); err == nil ||
			!strings.Contains(err.Error(), "bearer token is required") {
			t.Fatalf("expected token error, got %v", err)
		}
	})
	t.Run("invalid username", func(t *testing.T) {
		svc := xapi.NewService("tok", http.DefaultClient)
		for _, bad := range []string{"", "bad-user", strings.Repeat("a", 16), "has space"} {
			if _, err := svc.GetJSONFeed(bad); err == nil ||
				!strings.Contains(err.Error(), "invalid x.com username") {
				t.Fatalf("username %q: expected validation error, got %v", bad, err)
			}
		}
	})
}

func TestXAPI_GetJSONFeed_UpstreamErrors(t *testing.T) {
	t.Run("non-200 on user lookup", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		svc := xapi.NewService("tok", server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.GetJSONFeed("alice"); err == nil ||
			!strings.Contains(err.Error(), "status 401") {
			t.Fatalf("expected 401 upstream error, got %v", err)
		}
	})

	t.Run("user not found (empty data)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))
		defer server.Close()

		svc := xapi.NewService("tok", server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.GetJSONFeed("alice"); err == nil ||
			!strings.Contains(err.Error(), "x.com user not found") {
			t.Fatalf("expected user-not-found, got %v", err)
		}
	})

	t.Run("tweets endpoint error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/users/by/username/") {
				_, _ = w.Write([]byte(`{"data":{"id":"u1","username":"alice"}}`))
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		svc := xapi.NewService("tok", server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.GetJSONFeed("alice"); err == nil ||
			!strings.Contains(err.Error(), "status 429") {
			t.Fatalf("expected 429 error, got %v", err)
		}
	})
}

func TestXAPI_GetJSONFeed_EmptyTweetList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/users/by/username/") {
			_, _ = w.Write([]byte(`{"data":{"id":"u9","username":"bob","description":""}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	svc := xapi.NewService("tok", server.Client())
	svc.BaseURL = server.URL

	raw, err := svc.GetJSONFeed("bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("expected empty items, got %d", len(feed.Items))
	}
	if feed.Link != "https://x.com/bob" {
		t.Fatalf("feed link=%q", feed.Link)
	}
}
