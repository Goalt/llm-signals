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
	"github.com/Goalt/tg-channel-to-rss/internal/xapi"
)

func main() {
	host := envOrDefault("HOST", "0.0.0.0")
	port, err := strconv.Atoi(envOrDefault("PORT", "8000"))
	if err != nil {
		log.Fatalf("invalid PORT value: %v", err)
	}

	svc := app.NewService(http.DefaultClient)
	hyperliquidProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/hyperliquid",
		TargetBaseURL: envOrDefault("HYPERLIQUID_API_BASE_URL", "https://api.hyperliquid.xyz"),
		Authorization: os.Getenv("HYPERLIQUID_AUTHORIZATION"),
		Name:          "hyperliquid",
	})
	if err != nil {
		log.Fatalf("failed to initialize hyperliquid proxy: %v", err)
	}
	polymarketProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/polymarket",
		TargetBaseURL: envOrDefault("POLYMARKET_API_BASE_URL", "https://clob.polymarket.com"),
		Authorization: os.Getenv("POLYMARKET_AUTHORIZATION"),
		Name:          "polymarket",
	})
	if err != nil {
		log.Fatalf("failed to initialize polymarket proxy: %v", err)
	}
	bybitProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/bybit",
		TargetBaseURL: envOrDefault("BYBIT_API_BASE_URL", "https://api.bybit.com"),
		Authorization: os.Getenv("BYBIT_AUTHORIZATION"),
		Name:          "bybit",
	})
	if err != nil {
		log.Fatalf("failed to initialize bybit proxy: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if matchesProxyRoute(r.URL.Path, "/proxy/hyperliquid") {
			hyperliquidProxy.ServeHTTP(w, r)
			return
		}
		if matchesProxyRoute(r.URL.Path, "/proxy/polymarket") {
			polymarketProxy.ServeHTTP(w, r)
			return
		}
		if matchesProxyRoute(r.URL.Path, "/proxy/bybit") {
			bybitProxy.ServeHTTP(w, r)
			return
		}

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
	startXNotifier(ctx)

	addr := host + ":" + strconv.Itoa(port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Serving tg-channel-to-rss on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received, draining connections...")
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
		if closeErr := srv.Close(); closeErr != nil {
			log.Printf("forced close error: %v", closeErr)
		}
	}
	if err := <-serverErr; err != nil {
		log.Printf("server exited with error: %v", err)
	}
	log.Printf("server stopped")
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

func startXNotifier(ctx context.Context) {
	users := splitList(os.Getenv("X_USERS"))
	webhooks := splitList(os.Getenv("WEBHOOKS"))
	token := strings.TrimSpace(os.Getenv("X_BEARER_TOKEN"))

	if len(users) == 0 || len(webhooks) == 0 {
		log.Printf("x.com notifier disabled: set X_USERS and WEBHOOKS to enable")
		return
	}
	if token == "" {
		log.Printf("x.com notifier disabled: set X_BEARER_TOKEN")
		return
	}

	interval, err := time.ParseDuration(envOrDefault("X_POLL_INTERVAL", "5m"))
	if err != nil {
		log.Fatalf("invalid X_POLL_INTERVAL: %v", err)
	}

	fetcher := xapi.NewService(token, nil)
	n := notifier.New(notifier.Config{
		Channels:    users,
		Webhooks:    webhooks,
		Interval:    interval,
		HTTPTimeout: 30 * time.Second,
	}, fetcher, nil, nil)

	log.Printf("x.com notifier: polling %d user(s) every %s, dispatching to %d webhook(s)", len(users), interval, len(webhooks))
	go func() {
		if err := n.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("x.com notifier stopped: %v", err)
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
