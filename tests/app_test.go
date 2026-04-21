package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

// sampleChannelHTML returns a minimal Telegram /s/<channel> response with N
// posts whose IDs run from 1..n. Every bubble has a photo, text and timestamp.
func sampleChannelHTML(channel string, n int) string {
	var b strings.Builder
	b.WriteString(`<html><head><title>` + channel + ` channel</title>`)
	b.WriteString(`<meta property="og:description" content="desc of ` + channel + `"/></head><body>`)
	b.WriteString(`<div class="tgme_channel_info"><div class="tgme_channel_info_header_title">` + channel + `</div></div>`)
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		b.WriteString(`<div class="tgme_widget_message_bubble">`)
		b.WriteString(`<a class="tgme_widget_message_date" href="https://t.me/` + channel + `/` + id + `">d</a>`)
		b.WriteString(`<time class="time" datetime="2026-04-0` + id + `T10:00:00+00:00"></time>`)
		b.WriteString(`<div class="tgme_widget_message_text">post ` + id + ` see https://example.com/` + id + `</div>`)
		b.WriteString(`<a class="tgme_widget_message_photo_wrap" style="background-image:url('https://cdn.example.com/` + id + `.jpg')"></a>`)
		b.WriteString(`</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func TestApp_HandleFeedRequest_Validation(t *testing.T) {
	svc := app.NewService(http.DefaultClient)

	cases := []struct {
		name    string
		channel string
		wantMsg string
	}{
		{"empty", "", "Missing channel_name"},
		{"whitespace", "   ", "Missing channel_name"},
		{"too short", "abcd", "Invalid channel_name"},
		{"too long", strings.Repeat("a", 33), "Invalid channel_name"},
		{"bad chars", "hello-world", "Invalid channel_name"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body, headers := svc.HandleFeedRequest(c.channel)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", status)
			}
			if body != c.wantMsg {
				t.Fatalf("body=%q, want %q", body, c.wantMsg)
			}
			if headers["Content-Type"] != "text/plain; charset=UTF-8" {
				t.Fatalf("unexpected content-type: %q", headers["Content-Type"])
			}
		})
	}
}

func TestApp_HandleFeedRequest_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/mychannel" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sampleChannelHTML("mychannel", 3)))
	}))
	defer server.Close()

	svc := app.NewService(server.Client())
	svc.BaseURL = server.URL
	svc.Now = func() time.Time { return time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC) }

	status, body, headers := svc.HandleFeedRequest("mychannel")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%q", status, body)
	}
	if got := headers["Content-Type"]; got != "application/json; charset=UTF-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := headers["Cache-Control"]; got != "max-age=60, public" {
		t.Fatalf("cache-control=%q", got)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(body), &feed); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(feed.Items) != 3 {
		t.Fatalf("want 3 items, got %d", len(feed.Items))
	}
	for i, it := range feed.Items {
		if it.ID == "" || it.Link == "" {
			t.Errorf("item %d missing ID/Link: %+v", i, it)
		}
		if !strings.Contains(it.Description, "<p>") {
			t.Errorf("item %d description not wrapped in <p>: %q", i, it.Description)
		}
		if it.Enclosure == nil || it.Enclosure.Type != "image/jpeg" {
			t.Errorf("item %d expected jpeg enclosure, got %+v", i, it.Enclosure)
		}
	}
}

func TestApp_GetJSONFeed_ChannelNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := app.NewService(server.Client())
	svc.BaseURL = server.URL

	if _, err := svc.GetJSONFeed("ghost1"); err == nil ||
		!strings.Contains(err.Error(), "Telegram channel not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestApp_GetJSONFeed_ChannelNotFoundContactPage(t *testing.T) {
	// Telegram returns HTTP 200 with "Contact @..." page for non-existent channels
	// The key difference is the absence of .tgme_channel_info div
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
	<title>Telegram: Contact @nonexistentchannel123</title>
	<meta property="og:description" content="Contact @nonexistentchannel123 on Telegram">
</head>
<body>
	<div class="tgme_page">
		<div class="tgme_page_description">Contact @nonexistentchannel123 on Telegram</div>
	</div>
</body>
</html>`))
	}))
	defer server.Close()

	svc := app.NewService(server.Client())
	svc.BaseURL = server.URL

	if _, err := svc.GetJSONFeed("nonexistentchannel123"); err == nil ||
		!strings.Contains(err.Error(), "Telegram channel not found") {
		t.Fatalf("expected not-found error for contact page, got %v", err)
	}
}

func TestApp_GetJSONFeed_NetworkError(t *testing.T) {
	// Point at a closed server so the HTTP client errors out immediately.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	svc := app.NewService(&http.Client{Timeout: 500 * time.Millisecond})
	svc.BaseURL = url

	if _, err := svc.GetJSONFeed("testch1"); err == nil {
		t.Fatalf("expected network error")
	}
}

func TestApp_GetJSONFeed_EmptyChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>empty</title></head><body><div class="tgme_channel_info"></div></body></html>`))
	}))
	defer server.Close()

	svc := app.NewService(server.Client())
	svc.BaseURL = server.URL

	raw, err := svc.GetJSONFeed("empty1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if feed.Title != "empty" {
		t.Fatalf("title=%q", feed.Title)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(feed.Items))
	}
}

func TestApp_GetJSONFeed_SkipsReactionImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<html><head><title>c</title></head><body>
  <div class="tgme_channel_info"></div>
  <div class="tgme_widget_message_bubble">
    <a class="tgme_widget_message_date" href="https://t.me/chanx/1">d</a>
    <div class="tgme_widget_message_text">hi</div>
    <div class="tgme_widget_message_reactions">
      <img src="https://cdn.example.com/reaction.png"/>
    </div>
    <i class="emoji" style="background-image:url('https://cdn.example.com/emoji.png')"></i>
    <img src="https://cdn.example.com/real.jpg"/>
  </div>
</body></html>`))
	}))
	defer server.Close()

	svc := app.NewService(server.Client())
	svc.BaseURL = server.URL

	raw, err := svc.GetJSONFeed("chanx1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var feed app.FeedJSON
	_ = json.Unmarshal([]byte(raw), &feed)
	if len(feed.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(feed.Items))
	}
	enc := feed.Items[0].Enclosure
	if enc == nil || !strings.HasSuffix(enc.URL, "/real.jpg") {
		t.Fatalf("expected only real.jpg enclosure, got %+v", enc)
	}
	if strings.Contains(feed.Items[0].Content, "reaction.png") ||
		strings.Contains(feed.Items[0].Content, "emoji.png") {
		t.Fatalf("reaction/emoji images leaked into content: %s", feed.Items[0].Content)
	}
}
