// Command notifier-e2e drives internal/notifier against a real HTTP webhook
// receiver to manually verify dispatch behaviour end-to-end.
//
// It runs four scenarios back-to-back:
//  1. Seed pass: existing items MUST NOT be dispatched.
//  2. Two new items × two webhooks = four deliveries.
//  3. Unchanged feed on next tick = zero additional deliveries.
//  4. Webhook returning 500 is retried-per-item=once, logged, not fatal.
//
// Output is printed to stdout; the tests/manual/run_notifier.sh wrapper
// captures this into tests/manual/notifier.log.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
)

type fakeFetcher struct {
	mu    sync.Mutex
	items map[string][]app.FeedItemJSON
	errs  map[string]error
	calls map[string]int
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		items: map[string][]app.FeedItemJSON{},
		errs:  map[string]error{},
		calls: map[string]int{},
	}
}

func (f *fakeFetcher) GetJSONFeed(ch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[ch]++
	if err := f.errs[ch]; err != nil {
		return "", err
	}
	b, err := json.Marshal(app.FeedJSON{
		Title: ch,
		Link:  "https://t.me/s/" + ch,
		Items: f.items[ch],
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (f *fakeFetcher) set(ch string, items []app.FeedItemJSON) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[ch] = items
}

func (f *fakeFetcher) setErr(ch string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[ch] = err
}

type receiver struct {
	name   string
	status int
	mu     sync.Mutex
	hits   []notifier.Payload
	srv    *httptest.Server
}

func newReceiver(name string, status int) *receiver {
	r := &receiver{name: name, status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var p notifier.Payload
		if err := json.Unmarshal(body, &p); err != nil {
			log.Printf("[%s] bad json: %v", r.name, err)
		}
		r.mu.Lock()
		r.hits = append(r.hits, p)
		count := len(r.hits)
		r.mu.Unlock()
		log.Printf("[%s] ← POST %s  ct=%q  #%d item=%s",
			r.name, req.URL.Path, req.Header.Get("Content-Type"), count, p.Item.ID)
		w.WriteHeader(r.status)
	}))
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hits)
}

func (r *receiver) close() { r.srv.Close() }

func section(title string) {
	fmt.Println()
	fmt.Println("================================================================")
	fmt.Println("== " + title)
	fmt.Println("================================================================")
}

func waitFor(what string, deadline time.Duration, check func() bool) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if check() {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	log.Printf("TIMEOUT waiting for: %s", what)
	return false
}

var failures int32

func assert(cond bool, msg string, args ...any) {
	if !cond {
		atomic.AddInt32(&failures, 1)
		log.Printf("FAIL: "+msg, args...)
	} else {
		log.Printf("PASS: "+msg, args...)
	}
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// ── receivers ───────────────────────────────────────────────────────────
	hookA := newReceiver("hookA", http.StatusOK)
	defer hookA.close()
	hookB := newReceiver("hookB", http.StatusAccepted)
	defer hookB.close()
	hookC := newReceiver("hookC-500", http.StatusInternalServerError)
	defer hookC.close()

	// ── scenario 1+2+3 — happy path with 2 receivers ────────────────────────
	section("Scenario 1-3: seed, new-item dispatch, idle tick")
	fetcher := newFakeFetcher()
	fetcher.set("chanA", []app.FeedItemJSON{
		{ID: "old-1", Link: "https://t.me/chanA/1", Title: "seed"},
	})

	n1 := notifier.New(notifier.Config{
		SourceType:  notifier.SourceTypeTelegram,
		Channels:    []string{"chanA"},
		Webhooks:    []string{hookA.srv.URL, hookB.srv.URL},
		Interval:    100 * time.Millisecond,
		HTTPTimeout: 3 * time.Second,
	}, fetcher, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n1.Run(ctx) }()

	// Seed pass happens synchronously inside Run() before first ticker fires.
	// Give it a beat, then assert no deliveries.
	time.Sleep(200 * time.Millisecond)
	fetcher.mu.Lock()
	seedCalls := fetcher.calls["chanA"]
	fetcher.mu.Unlock()
	assert(seedCalls >= 1, "seed pass polled the channel at least once (calls=%d)", seedCalls)
	assert(hookA.count() == 0, "seed pass did NOT dispatch to hookA (got %d)", hookA.count())
	assert(hookB.count() == 0, "seed pass did NOT dispatch to hookB (got %d)", hookB.count())

	// Append two new items → expect 2×2=4 deliveries.
	fetcher.set("chanA", []app.FeedItemJSON{
		{ID: "old-1", Link: "https://t.me/chanA/1", Title: "seed"},
		{ID: "new-2", Link: "https://t.me/chanA/2", Title: "two"},
		{ID: "new-3", Link: "https://t.me/chanA/3", Title: "three"},
	})
	ok := waitFor("4 deliveries across hookA+hookB", 3*time.Second,
		func() bool { return hookA.count() >= 2 && hookB.count() >= 2 })
	assert(ok, "received 2 deliveries on each webhook (hookA=%d hookB=%d)",
		hookA.count(), hookB.count())

	before := hookA.count() + hookB.count()
	time.Sleep(350 * time.Millisecond) // 3 more ticks worth
	after := hookA.count() + hookB.count()
	assert(before == after, "idle ticks triggered no extra deliveries (before=%d after=%d)", before, after)

	// Verify payload shape on hookA.
	hookA.mu.Lock()
	if len(hookA.hits) >= 2 {
		p := hookA.hits[0]
		assert(p.Channel == "chanA", "payload channel=%q want chanA", p.Channel)
		assert(p.Item.ID == "new-2" || p.Item.ID == "new-3",
			"payload item ID=%q is one of the new ones", p.Item.ID)
	}
	hookA.mu.Unlock()

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("notifier Run returned: %v", err)
	}

	// ── scenario 4 — webhook 500 + fetch error on another channel ───────────
	section("Scenario 4: 500 receiver + fetch error on another channel")
	fetcher2 := newFakeFetcher()
	fetcher2.setErr("bad", errors.New("simulated upstream failure"))
	fetcher2.set("ok", []app.FeedItemJSON{
		{ID: "o-1", Link: "https://t.me/ok/1"},
	})

	n2 := notifier.New(notifier.Config{
		SourceType:  notifier.SourceTypeTelegram,
		Channels:    []string{"bad", "ok"},
		Webhooks:    []string{hookC.srv.URL},
		Interval:    100 * time.Millisecond,
		HTTPTimeout: 3 * time.Second,
	}, fetcher2, nil, nil)

	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- n2.Run(ctx2) }()

	time.Sleep(200 * time.Millisecond) // seed
	fetcher2.set("ok", []app.FeedItemJSON{
		{ID: "o-1", Link: "https://t.me/ok/1"},
		{ID: "o-2", Link: "https://t.me/ok/2"},
	})

	ok = waitFor("at least 1 delivery to failing hookC", 3*time.Second,
		func() bool { return hookC.count() >= 1 })
	assert(ok, "failing webhook was still attempted (hookC=%d)", hookC.count())

	fetcher2.mu.Lock()
	assert(fetcher2.calls["bad"] >= 2, "bad channel kept being polled despite errors (calls=%d)", fetcher2.calls["bad"])
	assert(fetcher2.calls["ok"] >= 2, "ok channel kept being polled (calls=%d)", fetcher2.calls["ok"])
	fetcher2.mu.Unlock()

	cancel2()
	if err := <-done2; err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("notifier2 Run returned: %v", err)
	}

	// ── scenario 5 — config validation ──────────────────────────────────────
	section("Scenario 5: Config validation via Run()")
	cases := []struct {
		name string
		cfg  notifier.Config
	}{
		{"no channels", notifier.Config{SourceType: notifier.SourceTypeTelegram, Webhooks: []string{"http://x"}, Interval: time.Second}},
		{"no webhooks", notifier.Config{SourceType: notifier.SourceTypeTelegram, Channels: []string{"a"}, Interval: time.Second}},
		{"non-positive interval", notifier.Config{SourceType: notifier.SourceTypeTelegram, Channels: []string{"a"}, Webhooks: []string{"http://x"}}},
	}
	for _, c := range cases {
		n := notifier.New(c.cfg, newFakeFetcher(), nil, nil)
		err := n.Run(context.Background())
		assert(err != nil, "config %q rejected with error: %v", c.name, err)
	}

	// ── summary ─────────────────────────────────────────────────────────────
	section("Summary")
	f := atomic.LoadInt32(&failures)
	if f == 0 {
		fmt.Println("ALL ASSERTIONS PASSED")
		os.Exit(0)
	}
	fmt.Printf("FAILURES: %d\n", f)
	os.Exit(1)
}
