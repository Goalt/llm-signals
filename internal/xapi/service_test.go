package xapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestGetJSONFeedFilteredStreamSharedBatchAndCaches(t *testing.T) {
	var streamCalls int32
	var rulesGetCalls int32
	var rulesPostCalls int32
	var aliceLookupCalls int32
	var bobLookupCalls int32
	rules := make(map[string]struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/by/username/alice":
			atomic.AddInt32(&aliceLookupCalls, 1)
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"alice","description":"alice profile"}}`))
		case r.URL.Path == "/users/by/username/bob":
			atomic.AddInt32(&bobLookupCalls, 1)
			_, _ = w.Write([]byte(`{"data":{"id":"43","username":"bob","description":"bob profile"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			atomic.AddInt32(&rulesGetCalls, 1)
			items := make([]string, 0, len(rules))
			for value := range rules {
				items = append(items, `{"id":"`+value+`","value":"`+value+`"}`)
			}
			_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			atomic.AddInt32(&rulesPostCalls, 1)
			body := readBody(t, r)
			if strings.Contains(body, `"value":"from:alice"`) {
				rules["from:alice"] = struct{}{}
			}
			if strings.Contains(body, `"value":"from:bob"`) {
				rules["from:bob"] = struct{}{}
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1"}]}`))
		case r.URL.Path == "/tweets/search/stream":
			atomic.AddInt32(&streamCalls, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello alice\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"alice\"}]}}\n\n"))
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"200\",\"text\":\"hello bob\",\"created_at\":\"2026-04-21T12:01:00Z\",\"author_id\":\"43\"},\"includes\":{\"users\":[{\"id\":\"43\",\"username\":\"bob\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true
	svc.FilteredStreamBatchWindow = 30 * time.Second

	rawAlice1, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("alice first call error: %v", err)
	}
	rawBob, err := svc.GetJSONFeed("bob")
	if err != nil {
		t.Fatalf("bob call error: %v", err)
	}
	rawAlice2, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("alice second call error: %v", err)
	}

	if atomic.LoadInt32(&streamCalls) != 1 {
		t.Fatalf("expected one shared stream request, got %d", streamCalls)
	}
	if atomic.LoadInt32(&aliceLookupCalls) != 1 || atomic.LoadInt32(&bobLookupCalls) != 1 {
		t.Fatalf("expected one user lookup per username, got alice=%d bob=%d", aliceLookupCalls, bobLookupCalls)
	}
	if atomic.LoadInt32(&rulesGetCalls) != 2 || atomic.LoadInt32(&rulesPostCalls) != 2 {
		t.Fatalf("expected two rules checks/adds for two unique usernames, got get=%d post=%d", rulesGetCalls, rulesPostCalls)
	}

	assertFeedHasItem(t, rawAlice1, "100", "https://x.com/alice/status/100")
	assertFeedHasItem(t, rawAlice2, "100", "https://x.com/alice/status/100")
	assertFeedHasItem(t, rawBob, "200", "https://x.com/bob/status/200")
}

func assertFeedHasItem(t *testing.T, raw string, wantID string, wantLink string) {
	t.Helper()
	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}
	if len(feed.Items) == 0 {
		t.Fatalf("expected at least one item")
	}
	if feed.Items[0].ID != wantID || feed.Items[0].Link != wantLink {
		t.Fatalf("unexpected first item: %+v", feed.Items[0])
	}
}

func TestUserCacheExpiration(t *testing.T) {
	var userLookupCalls int32
	var currentTime time.Time

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/by/username/alice":
			atomic.AddInt32(&userLookupCalls, 1)
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"alice","description":"alice profile"}}`))
		case r.URL.Path == "/users/42/tweets":
			_, _ = w.Write([]byte(`{"data":[{"id":"100","text":"hello","created_at":"2026-04-21T12:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UserCacheTTL = 10 * time.Second

	// Set time to 12:00:00
	currentTime = time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return currentTime }

	// First call: cache miss, should call lookup
	_, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if atomic.LoadInt32(&userLookupCalls) != 1 {
		t.Fatalf("expected one lookup, got %d", userLookupCalls)
	}

	// Second call at 12:00:05 (within TTL): should use cache
	currentTime = time.Date(2026, 4, 21, 12, 0, 5, 0, time.UTC)
	_, err = svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if atomic.LoadInt32(&userLookupCalls) != 1 {
		t.Fatalf("expected still one lookup (cached), got %d", userLookupCalls)
	}

	// Third call at 12:00:11 (TTL expired): should call lookup again
	currentTime = time.Date(2026, 4, 21, 12, 0, 11, 0, time.UTC)
	_, err = svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("third call error: %v", err)
	}
	if atomic.LoadInt32(&userLookupCalls) != 2 {
		t.Fatalf("expected two lookups (cache expired), got %d", userLookupCalls)
	}
}

func TestFilteredStreamBatchWindowExpiration(t *testing.T) {
	var streamCalls int32
	var rulesGetCalls int32
	var rulesPostCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/by/username/alice":
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"alice","description":"alice"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			atomic.AddInt32(&rulesGetCalls, 1)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			atomic.AddInt32(&rulesPostCalls, 1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1"}]}`))
		case r.URL.Path == "/tweets/search/stream":
			atomic.AddInt32(&streamCalls, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"alice\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true
	svc.FilteredStreamBatchWindow = 5 * time.Second

	// Set time to 10:00
	currentTime := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return currentTime }

	// First call: fetch batch
	_, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if atomic.LoadInt32(&streamCalls) != 1 {
		t.Fatalf("expected one stream fetch, got %d", streamCalls)
	}

	// Second call at 10:03 (within window): should reuse batch
	currentTime = time.Date(2026, 4, 21, 10, 0, 3, 0, time.UTC)
	_, err = svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if atomic.LoadInt32(&streamCalls) != 1 {
		t.Fatalf("expected still one stream fetch (batch fresh), got %d", streamCalls)
	}

	// Third call at 10:06 (window expired): should fetch new batch
	currentTime = time.Date(2026, 4, 21, 10, 0, 6, 0, time.UTC)
	_, err = svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("third call error: %v", err)
	}
	if atomic.LoadInt32(&streamCalls) != 2 {
		t.Fatalf("expected two stream fetches (batch expired), got %d", streamCalls)
	}
}

func TestFilteredStreamRuleCachingPerUsername(t *testing.T) {
	var rulesGetCalls int32
	var rulesPostCalls int32
	rules := make(map[string]struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/by/username/alice":
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"alice","description":"alice"}}`))
		case r.URL.Path == "/users/by/username/bob":
			_, _ = w.Write([]byte(`{"data":{"id":"43","username":"bob","description":"bob"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			atomic.AddInt32(&rulesGetCalls, 1)
			items := make([]string, 0, len(rules))
			for value := range rules {
				items = append(items, `{"id":"`+value+`","value":"`+value+`"}`)
			}
			_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			atomic.AddInt32(&rulesPostCalls, 1)
			body := readBody(t, r)
			if strings.Contains(body, `"value":"from:alice"`) {
				rules["from:alice"] = struct{}{}
			}
			if strings.Contains(body, `"value":"from:bob"`) {
				rules["from:bob"] = struct{}{}
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1"}]}`))
		case r.URL.Path == "/tweets/search/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"alice\"}]}}\n\n"))
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"200\",\"text\":\"hi\",\"created_at\":\"2026-04-21T12:01:00Z\",\"author_id\":\"43\"},\"includes\":{\"users\":[{\"id\":\"43\",\"username\":\"bob\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true
	svc.FilteredStreamBatchWindow = 30 * time.Second

	// First call with alice: should check/add rule
	_, err := svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("first alice call error: %v", err)
	}
	gets1 := atomic.LoadInt32(&rulesGetCalls)
	posts1 := atomic.LoadInt32(&rulesPostCalls)

	// Second call with alice (same batch window): should NOT re-check rule (it's cached in ensuredStreamRules)
	_, err = svc.GetJSONFeed("alice")
	if err != nil {
		t.Fatalf("second alice call error: %v", err)
	}
	gets2 := atomic.LoadInt32(&rulesGetCalls)
	posts2 := atomic.LoadInt32(&rulesPostCalls)
	if gets2 != gets1 || posts2 != posts1 {
		t.Fatalf("rule should be cached for same username, expected no additional calls, got get calls: %d->%d, post calls: %d->%d", gets1, gets2, posts1, posts2)
	}

	// Third call with bob: should check/add rule for bob (different username)
	_, err = svc.GetJSONFeed("bob")
	if err != nil {
		t.Fatalf("bob call error: %v", err)
	}
	gets3 := atomic.LoadInt32(&rulesGetCalls)
	posts3 := atomic.LoadInt32(&rulesPostCalls)
	if gets3 <= gets2 || posts3 <= posts2 {
		t.Fatalf("rule should be checked/added for new username, expected more calls, get: %d->%d, post: %d->%d", gets2, gets3, posts2, posts3)
	}
}

func TestFilteredStreamCaseInsensitiveUsernameHandling(t *testing.T) {
	var lookupCalls int32
	var rulesGetCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/users/by/username/") && r.Method == http.MethodGet:
			// Accept any case variation of alice
			atomic.AddInt32(&lookupCalls, 1)
			_, _ = w.Write([]byte(`{"data":{"id":"42","username":"alice","description":"alice"}}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodGet:
			atomic.AddInt32(&rulesGetCalls, 1)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/tweets/search/stream/rules" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":[{"id":"rule-1"}]}`))
		case r.URL.Path == "/tweets/search/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"data\":{\"id\":\"100\",\"text\":\"hello\",\"created_at\":\"2026-04-21T12:00:00Z\",\"author_id\":\"42\"},\"includes\":{\"users\":[{\"id\":\"42\",\"username\":\"alice\"}]}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("token123", server.Client())
	svc.BaseURL = server.URL
	svc.UseFilteredStream = true
	svc.FilteredStreamBatchWindow = 30 * time.Second

	// Call with "Alice" (mixed case)
	_, err := svc.GetJSONFeed("Alice")
	if err != nil {
		t.Fatalf("Alice call error: %v", err)
	}

	// Call with "ALICE" (uppercase) - should reuse cache and not do additional lookups/rule checks
	_, err = svc.GetJSONFeed("ALICE")
	if err != nil {
		t.Fatalf("ALICE call error: %v", err)
	}

	if atomic.LoadInt32(&lookupCalls) != 1 {
		t.Fatalf("expected one lookup (case-insensitive cache), got %d", lookupCalls)
	}
	if atomic.LoadInt32(&rulesGetCalls) != 1 {
		t.Fatalf("expected one rule check (case-insensitive cache), got %d", rulesGetCalls)
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
