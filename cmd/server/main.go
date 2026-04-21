package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
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

	addr := host + ":" + strconv.Itoa(port)
	log.Printf("Serving tg-channel-to-rss on http://%s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}
