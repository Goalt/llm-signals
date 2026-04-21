package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
)

func main() {
	host := envOrDefault("HOST", "0.0.0.0")
	port, err := strconv.Atoi(envOrDefault("PORT", "8000"))
	if err != nil {
		log.Fatalf("invalid PORT value: %v", err)
	}

	svc := app.NewService(http.DefaultClient)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, app.FeedPathPrefix) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}

		channelName := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, app.FeedPathPrefix), "/")
		status, body, headers := svc.HandleFeedRequest(channelName)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startNotifier(ctx, svc)

	addr := host + ":" + strconv.Itoa(port)
	log.Printf("Serving tg-channel-to-rss on http://%s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// startNotifier launches the webhook notifier in a background goroutine when
// TG_CHANNELS and WEBHOOKS are configured. When either is empty, the notifier
// is disabled and the server runs as a pure feed gateway.
func startNotifier(ctx context.Context, fetcher notifier.FeedFetcher) {
	channels := splitList(os.Getenv("TG_CHANNELS"))
	webhooks := splitList(os.Getenv("WEBHOOKS"))

	if len(channels) == 0 || len(webhooks) == 0 {
		log.Printf("notifier disabled: set TG_CHANNELS and WEBHOOKS to enable")
		return
	}

	interval, err := time.ParseDuration(envOrDefault("POLL_INTERVAL", "5m"))
	if err != nil {
		log.Fatalf("invalid POLL_INTERVAL: %v", err)
	}

	n := notifier.New(notifier.Config{
		Channels:    channels,
		Webhooks:    webhooks,
		Interval:    interval,
		HTTPTimeout: 30 * time.Second,
	}, fetcher, nil, nil)

	log.Printf("notifier: polling %d channel(s) every %s, dispatching to %d webhook(s)", len(channels), interval, len(webhooks))
	go func() {
		if err := n.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("notifier stopped: %v", err)
		}
	}()
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOrDefault(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}
