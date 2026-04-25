package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/openrouter"
	"github.com/Goalt/tg-channel-to-rss/internal/processanalyze"
)

type fakeProcessAnalyzeRunner struct {
	result processanalyze.Result
	err    error
	got    notifier.Payload
}

func (f *fakeProcessAnalyzeRunner) Process(_ context.Context, payload notifier.Payload) (processanalyze.Result, error) {
	f.got = payload
	return f.result, f.err
}

func TestProcessAnalyzeHandlerSuccess(t *testing.T) {
	runner := &fakeProcessAnalyzeRunner{result: processanalyze.Result{Status: "processed", Output: &openrouter.AnalysisResult{Action: "Купить"}}}
	h := newProcessAnalyzeHandlerWithRunner(runner)
	req := httptest.NewRequest(http.MethodPost, "/process-analyze", strings.NewReader(`{"id":"delivery-1","source_type":"telegram","source_url":"https://t.me/s/test","item":{"content":"hello"}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if runner.got.ID != "delivery-1" {
		t.Fatalf("runner got wrong payload: %+v", runner.got)
	}
	var out processanalyze.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Status != "processed" || out.Output == nil {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestNewProcessAnalyzeHandlerInitializesSQLiteStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "process-analyze.db")
	t.Setenv("PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION", "Bearer test")
	t.Setenv("PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN", "123")
	t.Setenv("PROCESS_ANALYZE_TELEGRAM_CHAT_ID", "456")
	t.Setenv("PROCESS_ANALYZE_SQLITE_PATH", dbPath)

	_ = newProcessAnalyzeHandler()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite file to be initialized: %v", err)
	}
}

func TestProcessAnalyzeHandlerErrors(t *testing.T) {
	t.Run("bad method", func(t *testing.T) {
		h := newProcessAnalyzeHandlerWithRunner(&fakeProcessAnalyzeRunner{})
		req := httptest.NewRequest(http.MethodGet, "/process-analyze", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
	})

	t.Run("bad json", func(t *testing.T) {
		h := newProcessAnalyzeHandlerWithRunner(&fakeProcessAnalyzeRunner{})
		req := httptest.NewRequest(http.MethodPost, "/process-analyze", strings.NewReader(`{`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
	})

	t.Run("runner error", func(t *testing.T) {
		h := newProcessAnalyzeHandlerWithRunner(&fakeProcessAnalyzeRunner{err: errors.New("boom")})
		req := httptest.NewRequest(http.MethodPost, "/process-analyze", strings.NewReader(`{"id":"x","item":{"content":"hello"}}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "boom") {
			t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("disabled handler", func(t *testing.T) {
		h := disabledProcessAnalyzeHandler("missing env")
		req := httptest.NewRequest(http.MethodPost, "/process-analyze", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "missing env") {
			t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
		}
	})
}
