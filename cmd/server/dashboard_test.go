package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/processdb"
)

func TestDashboardHandlerRendersPage(t *testing.T) {
	rt := newDashboardRuntime(func(string) string { return "" }, []apiProxyConfig{{
		RoutePrefix:   "/proxy/hyperliquid",
		TargetBaseURL: "https://api.hyperliquid.xyz",
		Authorization: "Bearer token",
		Name:          "hyperliquid",
	}})
	h := newDashboardHandler(rt)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, datastarCDNURL) {
		t.Fatalf("dashboard page should include datastar script: %s", body)
	}
	for _, id := range []string{"dashboard-overview", "dashboard-runtime", "dashboard-news", "dashboard-sources", "dashboard-requests", "dashboard-proxies", "dashboard-config"} {
		if !strings.Contains(body, id) {
			t.Fatalf("dashboard page missing %s", id)
		}
	}
}

func TestDashboardPartialsRenderRuntimeData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "process-analyze.db")
	store := processdb.NewService(dbPath, "process_analyze_news")
	if err := store.AppendRow(context.Background(), processdb.Row{
		ID:        "delivery-1",
		Text:      "hello world",
		Type:      "telegram",
		Source:    "https://t.me/s/durov",
		Metadata:  `{"views":"10"}`,
		Action:    "hold",
		CreatedAt: "1",
		UpdatedAt: "1",
	}); err != nil {
		t.Fatalf("append sqlite row: %v", err)
	}
	defer store.Close()

	env := map[string]string{
		"TG_CHANNELS": "durov,telegram",
		"WEBHOOKS":    "https://hooks.example.com/a",
		"PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION": "Bearer x",
		"PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN":       "123",
		"PROCESS_ANALYZE_TELEGRAM_CHAT_ID":         "1",
		"PROCESS_ANALYZE_SQLITE_PATH":              dbPath,
	}
	rt := newDashboardRuntime(func(name string) string { return env[name] }, []apiProxyConfig{{
		RoutePrefix:   "/proxy/bybit",
		TargetBaseURL: "https://api.bybit.com",
		Authorization: "",
		Name:          "bybit",
	}})
	rt.startedAt = time.Date(2026, 4, 25, 15, 0, 0, 0, time.UTC)
	rt.now = func() time.Time { return time.Date(2026, 4, 25, 15, 5, 0, 0, time.UTC) }
	rt.recordRequest(dashboardRequest{At: rt.startedAt.Add(time.Minute), Method: http.MethodGet, Path: "/feed/durov", Group: "feed", Status: http.StatusOK, Duration: 25 * time.Millisecond})
	rt.recordRequest(dashboardRequest{At: rt.startedAt.Add(2 * time.Minute), Method: http.MethodGet, Path: "/proxy/bybit/v5/market/time", Group: "proxy:bybit", Status: http.StatusBadGateway, Duration: 50 * time.Millisecond})

	h := newDashboardHandler(rt)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Total requests", "Telegram notifier", "/proxy/bybit", "PROCESS_ANALYZE_STATUS", "Stored news", "dashboard-news", "Go and SQLite metrics", "SQLite rows", "sqlite accessible", "process_analyze_news", "Go runtime", "SQLite"} {
		if !strings.Contains(body, want) {
			t.Fatalf("partials should include %q; body=%s", want, body)
		}
	}
}

func TestDashboardNewsPartialFiltersAndPagination(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "process-analyze.db")
	store := processdb.NewService(dbPath, "process_analyze_news")
	rows := []processdb.Row{
		{ID: "news-1", Text: "first telegram item", Type: "telegram", Source: "https://t.me/s/durov/1", Metadata: `{"views":"1"}`, Action: "hold", CreatedAt: "1000", UpdatedAt: "1000"},
		{ID: "news-2", Text: "second x item", Type: "x", Source: "https://x.com/i/2", Metadata: `{"views":"2"}`, Action: "sell", CreatedAt: "2000", UpdatedAt: "2000"},
		{ID: "news-3", Text: "third telegram item", Type: "telegram", Source: "https://t.me/s/durov/3", Metadata: `{"views":"3"}`, Action: "buy", CreatedAt: "3000", UpdatedAt: "3000"},
		{ID: "news-4", Text: "fourth x item", Type: "x", Source: "https://x.com/i/4", Metadata: `{"views":"4"}`, Action: "hold", CreatedAt: "4000", UpdatedAt: "4000"},
	}
	for _, row := range rows {
		if err := store.AppendRow(context.Background(), row); err != nil {
			t.Fatalf("append row %s: %v", row.ID, err)
		}
	}
	t.Cleanup(func() { _ = store.Close() })

	env := map[string]string{
		"TG_CHANNELS": "durov,telegram",
		"WEBHOOKS":    "https://hooks.example.com/a",
		"PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION": "Bearer x",
		"PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN":       "123",
		"PROCESS_ANALYZE_TELEGRAM_CHAT_ID":         "1",
		"PROCESS_ANALYZE_SQLITE_PATH":              dbPath,
	}
	rt := newDashboardRuntime(func(name string) string { return env[name] }, nil)
	h := newDashboardHandler(rt)

	t.Run("type filter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/news?type=telegram&per_page=5", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"news-3", "news-1", "Showing 1-2 of 2 news row(s)", "page 1 of 1"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in body: %s", want, body)
			}
		}
		for _, want := range []string{"news-4", "news-2"} {
			if strings.Contains(body, want) {
				t.Fatalf("did not expect %q in filtered body: %s", want, body)
			}
		}
	})

	t.Run("pagination", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/news?page=2&per_page=2", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"news-2", "news-1", "Showing 3-4 of 4 news row(s)", "page 2 of 2"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in body: %s", want, body)
			}
		}
		for _, want := range []string{"news-4", "news-3"} {
			if strings.Contains(body, want) {
				t.Fatalf("did not expect %q in paged body: %s", want, body)
			}
		}
	})
}

func TestDashboardNewsWarnsWhenFileMissing(t *testing.T) {
	rt := newDashboardRuntime(func(name string) string {
		switch name {
		case "PROCESS_ANALYZE_SQLITE_PATH":
			return filepath.Join(t.TempDir(), "missing.db")
		case "PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION":
			return "Bearer x"
		case "PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN":
			return "123"
		case "PROCESS_ANALYZE_TELEGRAM_CHAT_ID":
			return "1"
		default:
			return ""
		}
	}, nil)

	h := newDashboardHandler(rt)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/news", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sqlite file not created yet") {
		t.Fatalf("expected missing-file notice, body=%s", rec.Body.String())
	}
}

func TestDashboardSourcesPartialFiltersAndPagination(t *testing.T) {
	env := map[string]string{
		"TG_CHANNELS":         "durov,telegram",
		"WEBHOOKS":            "https://hooks.example.com/a",
		"X_USERS":             "elonmusk",
		"POLYMARKET_CHANNELS": "markets",
		"PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION": "Bearer x",
		"PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN":       "123",
		"PROCESS_ANALYZE_TELEGRAM_CHAT_ID":         "1",
		"PROCESS_ANALYZE_SQLITE_PATH":              filepath.Join(t.TempDir(), "process-analyze.db"),
	}
	rt := newDashboardRuntime(func(name string) string { return env[name] }, nil)
	h := newDashboardHandler(rt)

	t.Run("state filter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/sources?state=disabled&per_page=2", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"x.com notifier", "Showing 1-1 of 1 source(s)", "page 1 of 1"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in body: %s", want, body)
			}
		}
		for _, want := range []string{`Telegram notifier</td><td class="mono">telegram`, `Polymarket notifier</td><td class="mono">polymarket`, `Process + Analyze</td><td class="mono">pipeline`} {
			if strings.Contains(body, want) {
				t.Fatalf("did not expect %q in filtered body: %s", want, body)
			}
		}
	})

	t.Run("pagination", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/partials/sources?page=2&per_page=2", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Polymarket notifier", "Process + Analyze", "Showing 3-4 of 4 source(s)", "page 2 of 2"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in body: %s", want, body)
			}
		}
		for _, want := range []string{`Telegram notifier</td><td class="mono">telegram`, `x.com notifier</td><td class="mono">x`} {
			if strings.Contains(body, want) {
				t.Fatalf("did not expect %q in paged body: %s", want, body)
			}
		}
	})
}

func TestDashboardRuntimeInstrumentTracksStatus(t *testing.T) {
	rt := newDashboardRuntime(func(string) string { return "" }, nil)
	h := rt.instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	snap := rt.snapshot()
	if snap.Total != 1 || snap.Errors != 1 {
		t.Fatalf("unexpected counters: %+v", snap)
	}
	if snap.GroupCounts["mcp"] != 1 {
		t.Fatalf("expected mcp counter, got %+v", snap.GroupCounts)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].Status != http.StatusBadGateway {
		t.Fatalf("unexpected recent requests: %+v", snap.Recent)
	}
}

func TestSQLiteMetricsWarnsWhenFileMissing(t *testing.T) {
	rt := newDashboardRuntime(func(name string) string {
		switch name {
		case "PROCESS_ANALYZE_SQLITE_PATH":
			return filepath.Join(t.TempDir(), "missing.db")
		default:
			return ""
		}
	}, nil)

	metrics := rt.sqliteMetrics()
	if metrics.State != "warning" {
		t.Fatalf("expected warning state, got %+v", metrics)
	}
	if !strings.Contains(metrics.Detail, "sqlite file not created yet") {
		t.Fatalf("unexpected detail: %+v", metrics)
	}
}
