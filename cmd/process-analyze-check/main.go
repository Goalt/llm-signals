package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/processanalyze"
)

func main() {
	cfg, missing := processanalyze.LoadConfigFromEnv(os.Getenv)
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing required env vars: %s\n", strings.Join(missing, ", "))
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	gammaSvc := cfg.NewGamma(nil)
	if err := gammaSvc.CheckAccess(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "gamma check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("gamma: ok")

	store := cfg.NewStore()
	defer func() { _ = store.Close() }()
	if err := store.CheckAccess(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "sqlite check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("sqlite: ok")

	telegramSvc := cfg.NewTelegram(nil)
	if err := telegramSvc.CheckAccess(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "telegram bot check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("telegram bot: ok")

	openrouterSvc := cfg.NewOpenRouter(nil)
	if err := openrouterSvc.CheckAccess(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "openrouter check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("openrouter: ok")
}
