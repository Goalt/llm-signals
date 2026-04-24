package xapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

// streamMock is a minimal in-memory stand-in for X's filtered-stream + rules
// endpoints. It records the rule payloads POSTed to it and writes pushed
// events to any active stream consumer one line at a time.
type streamMock struct {
	server *httptest.Server

	rulesMu     sync.Mutex
	activeRules []streamRule

	connected chan struct{}
	events    chan string // pre-serialized JSON events

	rulePostCount int32
}

func newStreamMock(t *testing.T, user userLookupResponse) *streamMock {
	t.Helper()
	m := &streamMock{
		connected: make(chan struct{}, 1),
		events:    make(chan string, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/users/by/username/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(user)
	})
	mux.HandleFunc("/tweets/search/stream/rules", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			m.rulesMu.Lock()
			defer m.rulesMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": m.activeRules})
		case http.MethodPost:
			atomic.AddInt32(&m.rulePostCount, 1)
			var body struct {
				Add []struct {
					Value string `json:"value"`
					Tag   string `json:"tag"`
				} `json:"add"`
				Delete struct {
					IDs []string `json:"ids"`
				} `json:"delete"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.rulesMu.Lock()
			if len(body.Delete.IDs) > 0 {
				m.activeRules = nil
			}
			for i, a := range body.Add {
				m.activeRules = append(m.activeRules, streamRule{
					ID:    fmt.Sprintf("rule-%d", i+1),
					Value: a.Value,
					Tag:   a.Tag,
				})
			}
			m.rulesMu.Unlock()
			_, _ = w.Write([]byte(`{"meta":{"summary":{"created":1}}}`))
		}
	})
	mux.HandleFunc("/tweets/search/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		select {
		case m.connected <- struct{}{}:
		default:
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case line, ok := <-m.events:
				if !ok {
					return
				}
				_, _ = w.Write([]byte(line + "\n"))
				flusher.Flush()
			}
		}
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(m.events)
		m.server.Close()
	})
	return m
}

func (m *streamMock) push(evt streamEvent) {
	b, _ := json.Marshal(evt)
	m.events <- string(b)
}

func TestStreamBuffersAndDrains(t *testing.T) {
	user := userLookupResponse{}
	user.Data.ID = "99"
	user.Data.Username = "TestUser"
	user.Data.Description = "hello world"

	mock := newStreamMock(t, user)

	stream := NewStream("token-abc", mock.server.Client())
	stream.BaseURL = mock.server.URL
	stream.InitialBackoff = 10 * time.Millisecond
	stream.MaxBackoff = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := stream.Start(ctx, []string{"TestUser"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-mock.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("stream never connected")
	}

	evt := streamEvent{}
	evt.Data.ID = "100"
	evt.Data.Text = "hi & bye"
	evt.Data.CreatedAt = "2026-04-21T12:00:00Z"
	evt.Data.AuthorID = "99"
	evt.MatchingRules = append(evt.MatchingRules, struct {
		ID  string `json:"id"`
		Tag string `json:"tag"`
	}{ID: "rule-1", Tag: "TestUser"})
	mock.push(evt)

	// Wait for the tweet to land in the buffer.
	deadline := time.Now().Add(2 * time.Second)
	var tweets []bufferedTweet
	var meta userMeta
	for time.Now().Before(deadline) {
		meta, tweets = stream.Drain("TestUser")
		if len(tweets) > 0 {
			break
		}
		// put back what we drained - if nothing, nothing to restore.
		time.Sleep(10 * time.Millisecond)
	}
	if len(tweets) != 1 {
		t.Fatalf("expected 1 buffered tweet, got %d", len(tweets))
	}
	if tweets[0].ID != "100" || tweets[0].Text != "hi & bye" {
		t.Fatalf("unexpected tweet payload: %+v", tweets[0])
	}
	if meta.Username != "TestUser" {
		t.Fatalf("unexpected meta username: %+v", meta)
	}

	// Second drain is empty: confirms buffer cleared after read.
	_, again := stream.Drain("TestUser")
	if len(again) != 0 {
		t.Fatalf("expected buffer to be cleared, got %d tweets", len(again))
	}
}

func TestServiceGetJSONFeedFromStream(t *testing.T) {
	stream := &Stream{
		buffers: map[string][]bufferedTweet{
			"testuser": {
				{ID: "100", Text: "hi & bye", CreatedAt: time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)},
			},
		},
		users: map[string]userMeta{
			"testuser": {ID: "99", Username: "TestUser", Description: "profile"},
		},
	}
	svc := NewService("token", http.DefaultClient)
	svc.Stream = stream
	svc.Now = func() time.Time { return time.Date(2026, 4, 21, 15, 0, 0, 0, time.UTC) }

	raw, err := svc.GetJSONFeed("TestUser")
	if err != nil {
		t.Fatalf("GetJSONFeed: %v", err)
	}
	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if feed.Link != "https://x.com/TestUser" {
		t.Fatalf("unexpected link: %q", feed.Link)
	}
	if len(feed.Items) != 1 || feed.Items[0].ID != "100" {
		t.Fatalf("unexpected items: %+v", feed.Items)
	}
	if !strings.Contains(feed.Items[0].Description, "hi &amp; bye") {
		t.Fatalf("expected escaped text in description, got %q", feed.Items[0].Description)
	}

	// Subsequent call drains nothing — buffer cleared.
	raw2, err := svc.GetJSONFeed("TestUser")
	if err != nil {
		t.Fatalf("GetJSONFeed: %v", err)
	}
	var feed2 app.FeedJSON
	if err := json.Unmarshal([]byte(raw2), &feed2); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(feed2.Items) != 0 {
		t.Fatalf("expected empty feed on second drain, got %d items", len(feed2.Items))
	}
}

func TestStreamStartValidatesUsernames(t *testing.T) {
	stream := NewStream("tok", http.DefaultClient)
	if err := stream.Start(context.Background(), []string{"bad-user"}); err == nil {
		t.Fatal("expected invalid username error")
	}
}
