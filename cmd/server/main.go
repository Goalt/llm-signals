package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	applog "github.com/Goalt/logger/logger"
	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/xapi"
)

// log is the process-wide structured logger. It is initialized in main() so
// tests of other entry points don't incur logger setup cost on import.
var log applog.Logger

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "test-webhook":
			os.Exit(runTestWebhook(os.Args[2:]))
		case "serve", "server":
			// explicit server mode; fall through to default startup
		case "-h", "--help", "help":
			printUsage(os.Stdout)
			return
		default:
			if !strings.HasPrefix(os.Args[1], "-") {
				fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
				printUsage(os.Stderr)
				os.Exit(2)
			}
		}
	}

	log = applog.New(
		envOrDefault("APP_ENV", "production"),
		envOrDefault("LOG_LEVEL", "info"),
		envOrDefault("RELEASE_VERSION", "unknown"),
		os.Stdout,
		envOrDefault("LOG_TRACE_LEVEL", "error"),
		nil,
	)

	ctx := context.Background()

	host := envOrDefault("HOST", "0.0.0.0")
	port, err := strconv.Atoi(envOrDefault("PORT", "8000"))
	if err != nil {
		log.Errorf(ctx, "invalid PORT value: %v", err)
		os.Exit(1)
	}

	svc := app.NewService(http.DefaultClient)
	hyperliquidProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/hyperliquid",
		TargetBaseURL: envOrDefault("HYPERLIQUID_API_BASE_URL", "https://api.hyperliquid.xyz"),
		Authorization: os.Getenv("HYPERLIQUID_AUTHORIZATION"),
		Name:          "hyperliquid",
	})
	if err != nil {
		log.Errorf(ctx, "failed to initialize hyperliquid proxy: %v", err)
		os.Exit(1)
	}
	polymarketProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/polymarket",
		TargetBaseURL: envOrDefault("POLYMARKET_API_BASE_URL", "https://clob.polymarket.com"),
		Authorization: os.Getenv("POLYMARKET_AUTHORIZATION"),
		Name:          "polymarket",
	})
	if err != nil {
		log.Errorf(ctx, "failed to initialize polymarket proxy: %v", err)
		os.Exit(1)
	}
	bybitProxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/bybit",
		TargetBaseURL: envOrDefault("BYBIT_API_BASE_URL", "https://api.bybit.com"),
		Authorization: os.Getenv("BYBIT_AUTHORIZATION"),
		Name:          "bybit",
	})
	if err != nil {
		log.Errorf(ctx, "failed to initialize bybit proxy: %v", err)
		os.Exit(1)
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

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startNotifier(runCtx, svc)
	startXNotifier(runCtx)

	addr := host + ":" + strconv.Itoa(port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Infof(runCtx, "Serving tg-channel-to-rss on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case <-runCtx.Done():
		log.Infof(runCtx, "shutdown signal received, draining connections...")
	case err := <-serverErr:
		if err != nil {
			log.Errorf(runCtx, "server error: %v", err)
			os.Exit(1)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Errorf(shutdownCtx, "graceful shutdown error: %v", err)
		if closeErr := srv.Close(); closeErr != nil {
			log.Errorf(shutdownCtx, "forced close error: %v", closeErr)
		}
	}
	if err := <-serverErr; err != nil {
		log.Errorf(shutdownCtx, "server exited with error: %v", err)
	}
	log.Infof(shutdownCtx, "server stopped")
}

// startNotifier launches the webhook notifier in a background goroutine when
// TG_CHANNELS and WEBHOOKS are configured. When either is empty, the notifier
// is disabled and the server runs as a pure feed gateway.
func startNotifier(ctx context.Context, fetcher notifier.FeedFetcher) {
	channels := splitList(os.Getenv("TG_CHANNELS"))
	webhooks := splitList(os.Getenv("WEBHOOKS"))

	if len(channels) == 0 || len(webhooks) == 0 {
		log.Infof(ctx, "notifier disabled: set TG_CHANNELS and WEBHOOKS to enable")
		return
	}

	interval, err := time.ParseDuration(envOrDefault("POLL_INTERVAL", "5m"))
	if err != nil {
		log.Errorf(ctx, "invalid POLL_INTERVAL: %v", err)
		os.Exit(1)
	}

	n := notifier.New(notifier.Config{
		Channels:    channels,
		Webhooks:    webhooks,
		Interval:    interval,
		HTTPTimeout: 30 * time.Second,
	}, fetcher, nil, log)

	log.Infof(ctx, "notifier: polling %d channel(s) every %s, dispatching to %d webhook(s)", len(channels), interval, len(webhooks))
	go func() {
		if err := n.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf(ctx, "notifier stopped: %v", err)
		}
	}()
}

func startXNotifier(ctx context.Context) {
	users := splitList(os.Getenv("X_USERS"))
	webhooks := splitList(os.Getenv("WEBHOOKS"))
	token := strings.TrimSpace(os.Getenv("X_BEARER_TOKEN"))

	if len(users) == 0 || len(webhooks) == 0 {
		log.Infof(ctx, "x.com notifier disabled: set X_USERS and WEBHOOKS to enable")
		return
	}
	if token == "" {
		log.Infof(ctx, "x.com notifier disabled: set X_BEARER_TOKEN")
		return
	}

	interval, err := time.ParseDuration(envOrDefault("X_POLL_INTERVAL", "5m"))
	if err != nil {
		log.Errorf(ctx, "invalid X_POLL_INTERVAL: %v", err)
		os.Exit(1)
	}

	minRequestInterval, err := time.ParseDuration(envOrDefault("X_MIN_REQUEST_INTERVAL", "1s"))
	if err != nil {
		log.Errorf(ctx, "invalid X_MIN_REQUEST_INTERVAL: %v", err)
		os.Exit(1)
	}

	fetcher := xapi.NewService(token, nil)
	if minRequestInterval > 0 {
		fetcher.Limiter = xapi.NewRateLimiter(minRequestInterval)
	}
	n := notifier.New(notifier.Config{
		Channels:    users,
		Webhooks:    webhooks,
		Interval:    interval,
		HTTPTimeout: 30 * time.Second,
	}, fetcher, nil, log)

	log.Infof(ctx, "x.com notifier: polling %d user(s) every %s, dispatching to %d webhook(s) (min request interval: %s)", len(users), interval, len(webhooks), minRequestInterval)
	go func() {
		if err := n.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf(ctx, "x.com notifier stopped: %v", err)
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

func printUsage(w *os.File) {
	fmt.Fprintf(w, `Usage: server [subcommand] [flags]

Subcommands:
  serve          Run the HTTP server (default when no subcommand is given).
  test-webhook   Send a synthetic notifier payload to the configured WEBHOOKS.
                 Run "server test-webhook -h" for available flags.

Environment variables are documented in .env.example.
`)
}
