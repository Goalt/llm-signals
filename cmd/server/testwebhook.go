package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
)

// runTestWebhook sends a synthetic notifier.Payload to one or more webhook URLs
// so operators can verify wiring without waiting for a real post.
//
// Usage:
//
//	go run ./cmd/server test-webhook [-url https://hook.example.com[,...]]
//	                                 [-channel name] [-text "body"] [-timeout 10s]
//
// If -url is omitted, the comma-separated WEBHOOKS env var is used.
func runTestWebhook(args []string) int {
	fs := flag.NewFlagSet("test-webhook", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		urlsFlag   = fs.String("url", "", "comma-separated webhook URL(s); defaults to $WEBHOOKS")
		channel    = fs.String("channel", "test-channel", "channel name placed in payload")
		text       = fs.String("text", "This is a test webhook from tg-channel-to-rss.", "post text placed in payload")
		title      = fs.String("title", "Test webhook", "post title placed in payload")
		link       = fs.String("link", "https://example.com/test-webhook", "post link placed in payload")
		idFlag     = fs.String("id", "", "post id placed in payload (default: timestamp-based)")
		sourceType = fs.String("source-type", notifier.SourceTypeTelegram, "source type placed in payload")
		sourceURL  = fs.String("source-url", "", "source URL placed in payload (default: https://t.me/s/<channel>)")
		deliveryID = fs.String("delivery-id", "", "unique delivery id placed in payload (default: timestamp-based)")
		timeout    = fs.Duration("timeout", 10*time.Second, "per-request HTTP timeout")
	)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	urlsRaw := *urlsFlag
	if strings.TrimSpace(urlsRaw) == "" {
		urlsRaw = os.Getenv("WEBHOOKS")
	}
	urls := splitList(urlsRaw)
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "test-webhook: no URLs provided (use -url or WEBHOOKS env)")
		return 2
	}

	id := *idFlag
	now := time.Now().UTC()
	if id == "" {
		id = fmt.Sprintf("test-%d", now.Unix())
	}

	srcURL := *sourceURL
	if srcURL == "" {
		srcURL = "https://t.me/s/" + *channel
	}

	delivery := *deliveryID
	if delivery == "" {
		delivery = fmt.Sprintf("test-delivery-%d", now.UnixNano())
	}

	payload := notifier.Payload{
		ID:         delivery,
		SourceType: *sourceType,
		SourceURL:  srcURL,
		Channel:    *channel,
		Item: app.FeedItemJSON{
			Title:       *title,
			Description: *text,
			Link:        *link,
			Created:     now,
			ID:          id,
			Content:     *text,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test-webhook: marshal payload: %v\n", err)
		return 1
	}

	client := &http.Client{Timeout: *timeout}
	ctx := context.Background()

	failures := 0
	for _, u := range urls {
		status, respBody, err := postJSON(ctx, client, u, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "test-webhook: %s -> error: %v\n", u, err)
			failures++
			continue
		}
		if status < 200 || status >= 300 {
			fmt.Fprintf(os.Stderr, "test-webhook: %s -> HTTP %d: %s\n", u, status, truncate(respBody, 512))
			failures++
			continue
		}
		fmt.Fprintf(os.Stdout, "test-webhook: %s -> HTTP %d OK (%d bytes sent)\n", u, status, len(body))
	}

	if failures > 0 {
		return 1
	}
	return 0
}

func postJSON(ctx context.Context, client *http.Client, url string, body []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return res.StatusCode, string(respBody), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
