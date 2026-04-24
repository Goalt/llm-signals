package polymarket

import (
"encoding/json"
"net/http"
"net/http/httptest"
"strings"
"testing"
"time"

"github.com/Goalt/tg-channel-to-rss/internal/app"
)

func TestGetJSONFeedSuccessAndStableItemIdentity(t *testing.T) {
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
if r.URL.Path != "/sampling-markets" {
t.Fatalf("unexpected path: %q", r.URL.Path)
}
if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
t.Fatalf("unexpected auth header: %q", auth)
}
_, _ = w.Write([]byte(`{"data":[{"condition_id":"cond-1","question_id":"qid-ignored","market_slug":"first-market","question":"First?","description":"A & B","accepting_order_timestamp":"2026-04-22T10:00:00Z"},{"question_id":"qid-2","question":"Second?","description":"","end_date_iso":"2026-04-25T12:00:00Z"}]}`))
}))
defer server.Close()

svc := NewService("Bearer test-token", server.Client())
svc.BaseURL = server.URL
svc.Now = func() time.Time { return time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC) }

raw, err := svc.GetJSONFeed("sampling-markets")
if err != nil {
t.Fatalf("unexpected error: %v", err)
}

var feed app.FeedJSON
if err := json.Unmarshal([]byte(raw), &feed); err != nil {
t.Fatalf("invalid json returned: %v", err)
}

if feed.Link != server.URL+"/sampling-markets" {
t.Fatalf("unexpected feed link: %q", feed.Link)
}
if len(feed.Items) != 2 {
t.Fatalf("expected 2 items, got %d", len(feed.Items))
}

if feed.Items[0].ID != "cond-1" {
t.Fatalf("expected condition_id to be used first, got %q", feed.Items[0].ID)
}
if feed.Items[0].Link != "https://polymarket.com/event/first-market" {
t.Fatalf("unexpected item link: %q", feed.Items[0].Link)
}
if !strings.Contains(feed.Items[0].Description, "A &amp; B") {
t.Fatalf("expected escaped description, got %q", feed.Items[0].Description)
}
if !feed.Items[0].Created.Equal(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)) {
t.Fatalf("unexpected created time: %v", feed.Items[0].Created)
}

if feed.Items[1].ID != "qid-2" {
t.Fatalf("expected question_id fallback, got %q", feed.Items[1].ID)
}
if feed.Items[1].Link != server.URL+"/sampling-markets" {
t.Fatalf("expected source link fallback, got %q", feed.Items[1].Link)
}
if feed.Items[1].Description != "<p>Second?</p>" {
t.Fatalf("unexpected fallback description: %q", feed.Items[1].Description)
}
}

func TestGetJSONFeedSkipsEntriesWithoutAnyID(t *testing.T) {
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
_, _ = w.Write([]byte(`{"data":[{"question":"No ids 1"},{"market_slug":"   ","condition_id":"","question_id":"","question":"No ids 2"},{"market_slug":"keep-this","question":"Valid"}]}`))
}))
defer server.Close()

svc := NewService("", server.Client())
svc.BaseURL = server.URL

raw, err := svc.GetJSONFeed("events")
if err != nil {
t.Fatalf("unexpected error: %v", err)
}

var feed app.FeedJSON
if err := json.Unmarshal([]byte(raw), &feed); err != nil {
t.Fatalf("invalid json returned: %v", err)
}
if len(feed.Items) != 1 {
t.Fatalf("expected only one valid item, got %d", len(feed.Items))
}
if feed.Items[0].ID != "keep-this" {
t.Fatalf("expected slug fallback id, got %q", feed.Items[0].ID)
}
}

func TestResolveEndpointMappingsAndInvalidInput(t *testing.T) {
svc := NewService("", http.DefaultClient)
svc.BaseURL = "https://clob.polymarket.com"

cases := []struct {
name         string
channel      string
wantEndpoint string
wantTitle    string
}{
{name: "sampling-markets alias", channel: "sampling-markets", wantEndpoint: "https://clob.polymarket.com/sampling-markets", wantTitle: "Polymarket sampling-markets"},
{name: "events alias", channel: "events", wantEndpoint: "https://clob.polymarket.com/sampling-markets", wantTitle: "Polymarket events"},
{name: "markets alias", channel: "markets", wantEndpoint: "https://clob.polymarket.com/markets", wantTitle: "Polymarket markets"},
{name: "custom endpoint keeps query", channel: "markets?active=true", wantEndpoint: "https://clob.polymarket.com/markets?active=true", wantTitle: "Polymarket markets?active=true"},
}

for _, tc := range cases {
t.Run(tc.name, func(t *testing.T) {
endpoint, source, title, err := svc.resolveEndpoint(tc.channel)
if err != nil {
t.Fatalf("unexpected error: %v", err)
}
if endpoint != tc.wantEndpoint || source != tc.wantEndpoint {
t.Fatalf("unexpected endpoint/source: %q %q", endpoint, source)
}
if title != tc.wantTitle {
t.Fatalf("unexpected title: %q", title)
}
})
}

if _, _, _, err := svc.resolveEndpoint("   "); err == nil {
t.Fatalf("expected empty endpoint validation error")
}

svc.BaseURL = "://bad-base"
if _, _, _, err := svc.resolveEndpoint("events"); err == nil {
t.Fatalf("expected base URL parse error")
}
}

func TestGetJSONFeedUpstreamErrors(t *testing.T) {
t.Run("non-200 status", func(t *testing.T) {
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
http.Error(w, "boom", http.StatusBadGateway)
}))
defer server.Close()

svc := NewService("", server.Client())
svc.BaseURL = server.URL

_, err := svc.GetJSONFeed("events")
if err == nil || !strings.Contains(err.Error(), "status 502") {
t.Fatalf("expected status error, got %v", err)
}
})

t.Run("decode error", func(t *testing.T) {
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
_, _ = w.Write([]byte(`{"data":`))
}))
defer server.Close()

svc := NewService("", server.Client())
svc.BaseURL = server.URL

_, err := svc.GetJSONFeed("events")
if err == nil {
t.Fatalf("expected decode error")
}
})
}
