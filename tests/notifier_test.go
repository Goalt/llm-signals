package tests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
)

type fakeFetcher struct {
	mu    sync.Mutex
	feeds map[string][]app.FeedItemJSON
	errs  map[string]error
	calls map[string]int
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		feeds: map[string][]app.FeedItemJSON{},
		errs:  map[string]error{},
		calls: map[string]int{},
	}
}

func (f *fakeFetcher) GetJSONFeed(channel string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[channel]++
	if err := f.errs[channel]; err != nil {
		return "", err
	}
	b, err := json.Marshal(app.FeedJSON{
		Title: channel,
		Link:  "https://t.me/s/" + channel,
		Items: f.feeds[channel],
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (f *fakeFetcher) set(channel string, items []app.FeedItemJSON) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feeds[channel] = items
}

func (f *fakeFetcher) setErr(channel string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[channel] = err
}

func TestNotifier_Run_ValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  notifier.Config
	}{
		{"missing channels", notifier.Config{Webhooks: []string{"http://x"}, Interval: time.Second}},
		{"missing webhooks", notifier.Config{Channels: []string{"a"}, Interval: time.Second}},
		{"non-positive interval", notifier.Config{Channels: []string{"a"}, Webhooks: []string{"http://x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := notifier.New(tc.cfg, newFakeFetcher(), http.DefaultClient, nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := n.Run(ctx); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestNotifier_EndToEnd_DispatchesOnlyNewItemsToAllWebhooks(t *testing.T) {
	var count int32
	var mu sync.Mutex
	var payloads []notifier.Payload

	hookA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("hookA non-POST: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json; charset=UTF-8" {
			t.Errorf("hookA bad content-type: %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		var p notifier.Payload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Errorf("hookA bad json: %v", err)
		}
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer hookA.Close()

	hookB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hookB.Close()

	ff := newFakeFetcher()
	ff.set("chanA", []app.FeedItemJSON{
		{ID: "id-1", Link: "https://t.me/chanA/1", Title: "old"},
	})

	n := notifier.New(notifier.Config{
		Channels: []string{"chanA"},
		Webhooks: []string{hookA.URL, hookB.URL},
		Interval: 50 * time.Millisecond,
	}, ff, http.DefaultClient, nil)

	// Start notifier in background; after seed pass we mutate the feed.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(done)
	}()

	// Give seed tick time to run.
	time.Sleep(30 * time.Millisecond)
	ff.set("chanA", []app.FeedItemJSON{
		{ID: "id-1", Link: "https://t.me/chanA/1", Title: "old"},
		{ID: "id-2", Link: "https://t.me/chanA/2", Title: "new"},
	})

	// Wait until both webhooks received their payload or timeout.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&count) < 2 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("timed out; deliveries=%d", atomic.LoadInt32(&count))
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) == 0 {
		t.Fatalf("no payloads captured on hookA")
	}
	if payloads[0].Channel != "chanA" || payloads[0].Item.ID != "id-2" {
		t.Fatalf("unexpected payload: %+v", payloads[0])
	}
}

func TestNotifier_FetchErrorDoesNotBlockOtherChannels(t *testing.T) {
	ff := newFakeFetcher()
	ff.setErr("bad", errors.New("boom"))
	ff.set("ok", []app.FeedItemJSON{
		{ID: "ok-1", Link: "https://t.me/ok/1"},
	})

	var delivered int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&delivered, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	n := notifier.New(notifier.Config{
		Channels: []string{"bad", "ok"},
		Webhooks: []string{hook.URL},
		Interval: 30 * time.Millisecond,
	}, ff, http.DefaultClient, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	ff.set("ok", []app.FeedItemJSON{
		{ID: "ok-1", Link: "https://t.me/ok/1"},
		{ID: "ok-2", Link: "https://t.me/ok/2"},
	})

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&delivered) < 1 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("delivery never happened; delivered=%d", atomic.LoadInt32(&delivered))
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done

	ff.mu.Lock()
	defer ff.mu.Unlock()
	if ff.calls["bad"] < 1 || ff.calls["ok"] < 1 {
		t.Fatalf("expected both channels polled at least once: %v", ff.calls)
	}
}

func TestNotifier_WebhookNon2xxIsLoggedNotFatal(t *testing.T) {
	ff := newFakeFetcher()
	ff.set("c", []app.FeedItemJSON{{ID: "1", Link: "https://t.me/c/1"}})

	var hits int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hook.Close()

	n := notifier.New(notifier.Config{
		Channels: []string{"c"},
		Webhooks: []string{hook.URL},
		Interval: 30 * time.Millisecond,
	}, ff, http.DefaultClient, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	ff.set("c", []app.FeedItemJSON{
		{ID: "1", Link: "https://t.me/c/1"},
		{ID: "2", Link: "https://t.me/c/2"},
	})

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&hits) < 1 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected at least one webhook attempt")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestNotifier_ItemWithoutIDUsesLinkAsKey(t *testing.T) {
	ff := newFakeFetcher()
	ff.set("c", []app.FeedItemJSON{
		{Link: "https://t.me/c/7"}, // ID empty → should dedup on Link
	})

	var hits int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	n := notifier.New(notifier.Config{
		Channels: []string{"c"},
		Webhooks: []string{hook.URL},
		Interval: 30 * time.Millisecond,
	}, ff, http.DefaultClient, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = n.Run(ctx)
		close(done)
	}()

	// Seed pass + one scheduled tick with identical items: no deliveries expected.
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("expected 0 deliveries on unchanged feed, got %d", got)
	}
}
