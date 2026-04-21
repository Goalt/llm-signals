package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

type stubFetcher struct {
	mu      sync.Mutex
	feeds   map[string][]app.FeedItemJSON
	errors  map[string]error
	callLog map[string]int
}

func (s *stubFetcher) GetJSONFeed(channel string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callLog[channel]++
	if err, ok := s.errors[channel]; ok && err != nil {
		return "", err
	}
	feed := app.FeedJSON{
		Title: channel,
		Link:  "https://t.me/s/" + channel,
		Items: s.feeds[channel],
	}
	b, err := json.Marshal(feed)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *stubFetcher) setItems(channel string, items []app.FeedItemJSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feeds[channel] = items
}

func newStub() *stubFetcher {
	return &stubFetcher{
		feeds:   map[string][]app.FeedItemJSON{},
		errors:  map[string]error{},
		callLog: map[string]int{},
	}
}

func TestRunValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no channels", Config{Webhooks: []string{"http://x"}, Interval: time.Second}},
		{"no webhooks", Config{Channels: []string{"a"}, Interval: time.Second}},
		{"no interval", Config{Channels: []string{"a"}, Webhooks: []string{"http://x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := New(c.cfg, newStub(), http.DefaultClient, nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := n.Run(ctx); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestSeedAndDispatchNewItems(t *testing.T) {
	type received struct {
		URL  string
		Body Payload
	}

	var mu sync.Mutex
	var got []received
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct == "" {
			t.Errorf("missing content-type")
		}
		body, _ := io.ReadAll(r.Body)
		var p Payload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("invalid json: %v", err)
		}
		mu.Lock()
		got = append(got, received{URL: r.URL.Path, Body: p})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	hook2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p Payload
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		got = append(got, received{URL: "hook2" + r.URL.Path, Body: p})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook2.Close()

	stub := newStub()
	stub.setItems("chan1", []app.FeedItemJSON{
		{ID: "https://t.me/chan1/1", Link: "https://t.me/chan1/1", Title: "p1"},
	})

	n := New(Config{
		Channels: []string{"chan1"},
		Webhooks: []string{hook.URL, hook2.URL},
		Interval: time.Hour, // not used; we drive ticks manually
	}, stub, http.DefaultClient, nil)

	ctx := context.Background()

	// Seed: existing items must NOT be dispatched.
	n.tick(ctx, true)
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("seed pass should not dispatch, got %d deliveries", len(got))
	}
	mu.Unlock()

	// Add a new item and a second, plus keep the old one — only new should be delivered.
	stub.setItems("chan1", []app.FeedItemJSON{
		{ID: "https://t.me/chan1/1", Link: "https://t.me/chan1/1", Title: "p1"},
		{ID: "https://t.me/chan1/2", Link: "https://t.me/chan1/2", Title: "p2"},
		{ID: "https://t.me/chan1/3", Link: "https://t.me/chan1/3", Title: "p3"},
	})
	n.tick(ctx, false)

	mu.Lock()
	defer mu.Unlock()
	// 2 new items × 2 webhooks = 4 deliveries
	if len(got) != 4 {
		t.Fatalf("expected 4 deliveries, got %d: %+v", len(got), got)
	}
	ids := map[string]int{}
	for _, r := range got {
		if r.Body.Channel != "chan1" {
			t.Errorf("wrong channel: %q", r.Body.Channel)
		}
		ids[r.Body.Item.ID]++
	}
	if ids["https://t.me/chan1/2"] != 2 || ids["https://t.me/chan1/3"] != 2 {
		t.Errorf("unexpected delivery distribution: %v", ids)
	}

	// Another tick without changes should not dispatch anything new.
	before := len(got)
	mu.Unlock()
	n.tick(ctx, false)
	mu.Lock()
	if len(got) != before {
		t.Errorf("unexpected deliveries after idle tick: %d -> %d", before, len(got))
	}
}

func TestFetchErrorIsLoggedNotFatal(t *testing.T) {
	stub := newStub()
	stub.errors["bad"] = io.EOF
	stub.setItems("good", []app.FeedItemJSON{
		{ID: "https://t.me/good/1", Link: "https://t.me/good/1"},
	})

	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	n := New(Config{
		Channels: []string{"bad", "good"},
		Webhooks: []string{hook.URL},
		Interval: time.Hour,
	}, stub, http.DefaultClient, nil)

	// Seed — both channels polled, no webhook fired.
	n.tick(context.Background(), true)
	// New item added for "good"
	stub.setItems("good", []app.FeedItemJSON{
		{ID: "https://t.me/good/1", Link: "https://t.me/good/1"},
		{ID: "https://t.me/good/2", Link: "https://t.me/good/2"},
	})
	// Should not panic / should deliver for "good" despite "bad" failing.
	n.tick(context.Background(), false)

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.callLog["bad"] < 2 || stub.callLog["good"] < 2 {
		t.Errorf("expected each channel polled twice, got %v", stub.callLog)
	}
}
