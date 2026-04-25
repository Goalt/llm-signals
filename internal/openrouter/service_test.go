package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePolymarketTool struct {
	lastTag string
	result  string
}

func (f *fakePolymarketTool) EventsByTag(_ context.Context, tag string) (string, error) {
	f.lastTag = tag
	return f.result, nil
}

func TestAnalyzeNewsToolFlow(t *testing.T) {
	var calls int
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		calls++
		switch calls {
		case 1:
			if !strings.Contains(string(body), `"get_polymarket_events"`) {
				t.Fatalf("expected tool definition in first request: %s", string(body))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"get_polymarket_events","arguments":"{\"tag\":\"politics\"}"}}]}}]}`))
		case 2:
			if !strings.Contains(string(body), `"tool_call_id":"call-1"`) {
				t.Fatalf("expected tool result in second request: %s", string(body))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"title\":\"Новая новость\",\"id\":\"news-1\",\"source\":\"https://t.me/s/source\",\"action\":\"Купить\",\"explanation\":\"Потому что рынок может отреагировать.\",\"tags\":\"#рынок #трамп\"}"}}]}`))
		default:
			t.Fatalf("unexpected extra call %d", calls)
		}
	}))
	defer server.Close()

	tool := &fakePolymarketTool{result: `{"data":[{"slug":"market-1"}]}`}
	svc := NewService("Bearer or-key", "model-x", server.Client())
	svc.BaseURL = server.URL
	svc.Polymarket = tool

	out, err := svc.AnalyzeNews(context.Background(), AnalyzeInput{ID: "news-1", Source: "https://t.me/s/source", Text: "text", Metadata: map[string]any{"foo": "bar"}})
	if err != nil {
		t.Fatalf("AnalyzeNews: %v", err)
	}
	if authHeader != "Bearer or-key" {
		t.Fatalf("unexpected auth header: %q", authHeader)
	}
	if tool.lastTag != "politics" {
		t.Fatalf("expected tool tag politics, got %q", tool.lastTag)
	}
	if out.Action != "Купить" || out.ID != "news-1" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestAnalyzeNewsDirectJSONAndDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"action\\\":\\\"Игнорировать новость\\\",\\\"explanation\\\":\\\"Слишком слабый сигнал.\\\",\\\"tags\\\":\\\"#рынок\\\"}\\n```\"}}]}"))
	}))
	defer server.Close()

	svc := NewService("Bearer or-key", "model-x", server.Client())
	svc.BaseURL = server.URL
	out, err := svc.AnalyzeNews(context.Background(), AnalyzeInput{ID: "news-2", Source: "src", Text: "very long title"})
	if err != nil {
		t.Fatalf("AnalyzeNews: %v", err)
	}
	if out.ID != "news-2" || out.Source != "src" || out.Title == "" {
		t.Fatalf("expected fallback fields, got %+v", out)
	}
}

func TestCheckAccessAndErrors(t *testing.T) {
	t.Run("check access", func(t *testing.T) {
		var req map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"ok\"}"}}]}`))
		}))
		defer server.Close()
		svc := NewService("Bearer or-key", "model-x", server.Client())
		svc.BaseURL = server.URL
		if err := svc.CheckAccess(context.Background()); err != nil {
			t.Fatalf("CheckAccess: %v", err)
		}
		if req["model"] != "model-x" {
			t.Fatalf("unexpected model: %#v", req["model"])
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusUnauthorized)
		}))
		defer server.Close()
		svc := NewService("Bearer or-key", "model-x", server.Client())
		svc.BaseURL = server.URL
		if _, err := svc.AnalyzeNews(context.Background(), AnalyzeInput{ID: "1", Source: "src", Text: "x"}); err == nil || !strings.Contains(err.Error(), "status 401") {
			t.Fatalf("expected http error, got %v", err)
		}
	})
}
