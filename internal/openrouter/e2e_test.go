//go:build e2e

package openrouter

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestOpenRouterCheckAccessE2E(t *testing.T) {
	auth := os.Getenv("PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION")
	if auth == "" {
		t.Skip("PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION not set")
	}
	model := os.Getenv("PROCESS_ANALYZE_OPENROUTER_MODEL")
	if model == "" {
		model = "tencent/hy3-preview:free"
	}
	svc := NewService(auth, model, nil)
	if base := os.Getenv("PROCESS_ANALYZE_OPENROUTER_API_BASE_URL"); base != "" {
		svc.BaseURL = base
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.CheckAccess(ctx); err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
}
