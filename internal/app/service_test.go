package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleFeedRequestValidation(t *testing.T) {
	svc := NewService(http.DefaultClient)

	status, body, _ := svc.HandleFeedRequest("")
	if status != http.StatusBadRequest || body != "Missing channel_name" {
		t.Fatalf("expected missing channel_name error, got status=%d body=%q", status, body)
	}

	status, body, _ = svc.HandleFeedRequest("ab")
	if status != http.StatusBadRequest || body != "Invalid channel_name" {
		t.Fatalf("expected invalid channel_name error, got status=%d body=%q", status, body)
	}
}

func TestGetJSONFeedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/testch1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`
<html>
  <head>
    <title>Test Channel</title>
    <meta property="og:description" content="Test Description" />
  </head>
  <body>
    <div class="tgme_widget_message_bubble">
      <a class="tgme_widget_message_date" href="https://t.me/testch1/123">date</a>
      <time class="time" datetime="2026-04-21T10:00:00+00:00"></time>
      <div class="tgme_widget_message_text">Visit https://example.com and hi</div>
      <img src="https://cdn.example.com/photo.jpg" />
    </div>
  </body>
</html>`))
	}))
	defer server.Close()

	svc := NewService(server.Client())
	svc.BaseURL = server.URL
	svc.Now = func() time.Time { return time.Date(2026, 4, 21, 15, 0, 0, 0, time.UTC) }

	feedJSON, err := svc.GetJSONFeed("testch1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{`"title":"Test Channel"`, `"description":"Test Description"`, `"link":"https://t.me/s/testch1/123"`, `"url":"https://cdn.example.com/photo.jpg"`}
	for _, check := range checks {
		if !strings.Contains(feedJSON, check) {
			t.Fatalf("expected feed json to contain %q", check)
		}
	}

	var parsed FeedJSON
	if err := json.Unmarshal([]byte(feedJSON), &parsed); err != nil {
		t.Fatalf("invalid json returned: %v", err)
	}
}

func TestGetJSONFeedChannelNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := NewService(server.Client())
	svc.BaseURL = server.URL

	_, err := svc.GetJSONFeed("testch1")
	if err == nil || err.Error() != "Telegram channel not found" {
		t.Fatalf("expected Telegram channel not found error, got %v", err)
	}
}

func TestGuessMIME(t *testing.T) {
	cases := map[string]string{
		"https://a/b.JPG":  "image/jpeg",
		"https://a/b.png":  "image/png",
		"https://a/b.webp": "image/webp",
		"https://a/b.gif":  "image/gif",
		"https://a/b.bin":  "application/octet-stream",
	}

	for input, expected := range cases {
		if actual := guessMIME(input); actual != expected {
			t.Fatalf("guessMIME(%q)=%q want %q", input, actual, expected)
		}
	}
}

func TestAutolinkPlain(t *testing.T) {
	text := `test https://example.com?q=1&x=2`
	got := autolinkPlain(text)
	if !strings.Contains(got, `<a href="https://example.com?q=1&amp;x=2"`) {
		t.Fatalf("expected linked url in output: %s", got)
	}
}
