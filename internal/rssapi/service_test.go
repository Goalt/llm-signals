package rssapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example Feed</title>
    <link>https://example.com/</link>
    <description>An example feed</description>
    <pubDate>Mon, 21 Apr 2026 12:00:00 +0000</pubDate>
    <item>
      <title>First Post</title>
      <link>https://example.com/posts/1</link>
      <guid>post-1</guid>
      <description>Hello &amp; world</description>
      <pubDate>Mon, 21 Apr 2026 12:00:00 +0000</pubDate>
      <enclosure url="https://example.com/a.mp3" length="1234" type="audio/mpeg"/>
    </item>
    <item>
      <title>Second Post</title>
      <link>https://example.com/posts/2</link>
      <description>Another one</description>
      <pubDate>Tue, 22 Apr 2026 08:30:00 +0000</pubDate>
    </item>
  </channel>
</rss>`

const sampleAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <subtitle>Atom subtitle</subtitle>
  <link href="https://atom.example.com/"/>
  <updated>2026-04-22T10:00:00Z</updated>
  <entry>
    <id>urn:uuid:1</id>
    <title>Atom Entry 1</title>
    <link href="https://atom.example.com/e1" rel="alternate"/>
    <published>2026-04-22T09:00:00Z</published>
    <updated>2026-04-22T09:30:00Z</updated>
    <summary>Short summary</summary>
    <content>Full content body</content>
  </entry>
</feed>`

func TestGetJSONFeedRSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rss" {
			http.NotFound(w, r)
			return
		}
		if accept := r.Header.Get("Accept"); !strings.Contains(accept, "rss+xml") {
			t.Errorf("expected RSS in Accept header, got %q", accept)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	svc := NewService(server.Client())
	svc.Now = func() time.Time { return time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC) }

	raw, err := svc.GetJSONFeed(server.URL + "/rss")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}

	if feed.Title != "Example Feed" {
		t.Errorf("unexpected title: %q", feed.Title)
	}
	if feed.Link != "https://example.com/" {
		t.Errorf("unexpected link: %q", feed.Link)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(feed.Items))
	}

	first := feed.Items[0]
	if first.ID != "post-1" {
		t.Errorf("expected guid as id, got %q", first.ID)
	}
	if first.Link != "https://example.com/posts/1" {
		t.Errorf("unexpected link: %q", first.Link)
	}
	if first.Title != "First Post" {
		t.Errorf("unexpected title: %q", first.Title)
	}
	if !strings.Contains(first.Content, "Hello & world") {
		t.Errorf("unexpected content: %q", first.Content)
	}
	if first.Enclosure == nil || first.Enclosure.URL != "https://example.com/a.mp3" {
		t.Errorf("unexpected enclosure: %+v", first.Enclosure)
	}
	want := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	if !first.Created.Equal(want) {
		t.Errorf("unexpected created: %v want %v", first.Created, want)
	}

	// Second item falls back to link as ID (no guid).
	if feed.Items[1].ID != "https://example.com/posts/2" {
		t.Errorf("expected link-as-id, got %q", feed.Items[1].ID)
	}
}

func TestGetJSONFeedAtom(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(sampleAtom))
	}))
	defer server.Close()

	svc := NewService(server.Client())
	svc.Now = func() time.Time { return time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC) }

	raw, err := svc.GetJSONFeed(server.URL + "/atom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}

	if feed.Title != "Atom Example" {
		t.Errorf("unexpected title: %q", feed.Title)
	}
	if feed.Link != "https://atom.example.com/" {
		t.Errorf("unexpected link: %q", feed.Link)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(feed.Items))
	}

	it := feed.Items[0]
	if it.ID != "urn:uuid:1" {
		t.Errorf("unexpected id: %q", it.ID)
	}
	if it.Link != "https://atom.example.com/e1" {
		t.Errorf("unexpected link: %q", it.Link)
	}
	if it.Content != "Full content body" {
		t.Errorf("expected content body, got %q", it.Content)
	}
	wantCreated := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	if !it.Created.Equal(wantCreated) {
		t.Errorf("unexpected created: %v want %v", it.Created, wantCreated)
	}
}

func TestGetJSONFeedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	svc := NewService(server.Client())
	if _, err := svc.GetJSONFeed(server.URL + "/rss"); err == nil {
		t.Fatalf("expected error for non-2xx response")
	}
}

func TestGetJSONFeedUnsupportedFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>nope</body></html>`))
	}))
	defer server.Close()

	svc := NewService(server.Client())
	if _, err := svc.GetJSONFeed(server.URL + "/rss"); err == nil {
		t.Fatalf("expected error for unsupported format")
	}
}

func TestGetJSONFeedInvalidURL(t *testing.T) {
	svc := NewService(nil)
	cases := []string{"", "   ", "not-a-url", "ftp://example.com/rss", "http://"}
	for _, c := range cases {
		if _, err := svc.GetJSONFeed(c); err == nil {
			t.Errorf("expected error for url %q", c)
		}
	}
}

type countingLimiter struct{ calls int }

func (c *countingLimiter) Wait() { c.calls++ }

func TestGetJSONFeedCallsLimiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleRSS))
	}))
	defer server.Close()

	lim := &countingLimiter{}
	svc := NewService(server.Client())
	svc.Limiter = lim
	if _, err := svc.GetJSONFeed(server.URL + "/rss"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lim.calls != 1 {
		t.Fatalf("expected limiter.Wait to be called once, got %d", lim.calls)
	}
}
