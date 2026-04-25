package main

import (
	"context"
	"os"
	"os/exec"
	"testing"

	applog "github.com/Goalt/logger/logger"
)

func TestStartPolymarketNotifierDisabledWithoutRequiredEnv(t *testing.T) {
	log = applog.New("test", "error", "test", os.Stdout, "error", nil)

	t.Setenv("POLYMARKET_CHANNELS", "")
	t.Setenv("WEBHOOKS", "")
	t.Setenv("POLYMARKET_POLL_INTERVAL", "")
	t.Setenv("POLYMARKET_MIN_REQUEST_INTERVAL", "")

	startPolymarketNotifier(context.Background())
}

func TestStartPolymarketNotifierInvalidPollIntervalExits(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		log = applog.New("test", "error", "test", os.Stdout, "error", nil)

		t.Setenv("POLYMARKET_CHANNELS", "events")
		t.Setenv("WEBHOOKS", "http://127.0.0.1/webhook")
		t.Setenv("POLYMARKET_POLL_INTERVAL", "not-a-duration")
		t.Setenv("POLYMARKET_MIN_REQUEST_INTERVAL", "1s")
		startPolymarketNotifier(context.Background())
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestStartPolymarketNotifierInvalidPollIntervalExits")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected process to exit non-zero")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}
