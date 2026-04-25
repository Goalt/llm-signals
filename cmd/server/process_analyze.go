package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/processanalyze"
)

type processAnalyzeRunner interface {
	Process(ctx context.Context, payload notifier.Payload) (processanalyze.Result, error)
}

func newProcessAnalyzeHandler() http.Handler {
	cfg, missing := processanalyze.LoadConfigFromEnv(os.Getenv)
	if len(missing) > 0 {
		return disabledProcessAnalyzeHandler("process-analyze disabled: set " + strings.Join(missing, ", "))
	}
	return newProcessAnalyzeHandlerWithRunner(cfg.NewProcessor(http.DefaultClient))
}

func newProcessAnalyzeHandlerWithRunner(runner processAnalyzeRunner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer func() { _ = r.Body.Close() }()

		var payload notifier.Payload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
			http.Error(w, "invalid process-analyze payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		result, err := runner.Process(r.Context(), payload)
		if err != nil {
			http.Error(w, fmt.Sprintf("process-analyze failed: %v", err), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

func disabledProcessAnalyzeHandler(reason string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, reason, http.StatusServiceUnavailable)
	})
}
