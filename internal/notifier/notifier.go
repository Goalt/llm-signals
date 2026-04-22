// Package notifier periodically polls public Telegram channels for new posts
// and forwards them to a configured list of webhooks.
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

// FeedFetcher fetches the current JSON feed for a single Telegram channel.
// It is satisfied by *app.Service but kept as an interface for testability.
type FeedFetcher interface {
	GetJSONFeed(channelName string) (string, error)
}

// Config describes notifier runtime parameters.
type Config struct {
	// Channels is the list of Telegram channel names to poll.
	Channels []string
	// Webhooks is the list of HTTP endpoints to POST new-post payloads to.
	Webhooks []string
	// Interval is the polling interval.
	Interval time.Duration
	// HTTPTimeout is the per-request timeout for webhook delivery.
	HTTPTimeout time.Duration
}

// Payload is the JSON body sent to each webhook for every new post.
type Payload struct {
	Channel string           `json:"channel"`
	Item    app.FeedItemJSON `json:"item"`
}

// Notifier polls Telegram channels and dispatches new posts to webhooks.
type Notifier struct {
	cfg     Config
	fetcher FeedFetcher
	client  *http.Client
	logger  *log.Logger

	mu   sync.Mutex
	seen map[string]map[string]struct{} // channel -> set of post IDs
}

// New creates a Notifier. If client is nil a default client with HTTPTimeout is used.
// If logger is nil the standard logger is used.
func New(cfg Config, fetcher FeedFetcher, client *http.Client, logger *log.Logger) *Notifier {
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Notifier{
		cfg:     cfg,
		fetcher: fetcher,
		client:  client,
		logger:  logger,
		seen:    make(map[string]map[string]struct{}),
	}
}

// Run starts the polling loop until ctx is cancelled.
// The first pass seeds the "seen" set without sending webhooks, so the notifier
// does not spam subscribers with historical posts on startup.
func (n *Notifier) Run(ctx context.Context) error {
	if len(n.cfg.Channels) == 0 {
		return fmt.Errorf("notifier: no channels configured")
	}
	if len(n.cfg.Webhooks) == 0 {
		return fmt.Errorf("notifier: no webhooks configured")
	}
	if n.cfg.Interval <= 0 {
		return fmt.Errorf("notifier: interval must be positive")
	}

	// Seed pass: record current items as already seen so we only send truly new posts.
	n.tick(ctx, true)

	ticker := time.NewTicker(n.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			n.tick(ctx, false)
		}
	}
}

// tick polls every channel once. When seed is true, new items are only recorded
// (not forwarded) so subsequent polls only report posts published after startup.
func (n *Notifier) tick(ctx context.Context, seed bool) {
	for _, channel := range n.cfg.Channels {
		if ctx.Err() != nil {
			return
		}
		n.pollChannel(ctx, channel, seed)
	}
}

func (n *Notifier) pollChannel(ctx context.Context, channel string, seed bool) {
	raw, err := n.fetcher.GetJSONFeed(channel)
	if err != nil {
		n.logger.Printf("notifier: fetch %q failed: %v", channel, err)
		return
	}

	var feed app.FeedJSON
	if err := json.Unmarshal([]byte(raw), &feed); err != nil {
		n.logger.Printf("notifier: decode %q failed: %v", channel, err)
		return
	}

	// Sort items by creation time descending so index 0 is the newest post
	// regardless of upstream ordering.
	sort.SliceStable(feed.Items, func(i, j int) bool {
		return feed.Items[i].Created.After(feed.Items[j].Created)
	})

	if len(feed.Items) > 0 {
		latest := feed.Items[0]
		id := latest.ID
		if id == "" {
			id = latest.Link
		}
		n.logger.Printf("notifier: debug %q latest item id=%q title=%q link=%q created=%s items=%d",
			channel, id, latest.Title, latest.Link, latest.Created.Format(time.RFC3339), len(feed.Items))
	} else {
		n.logger.Printf("notifier: debug %q feed has no items", channel)
	}

	n.mu.Lock()
	seen, ok := n.seen[channel]
	if !ok {
		seen = make(map[string]struct{}, len(feed.Items))
		n.seen[channel] = seen
	}

	newItems := make([]app.FeedItemJSON, 0)
	// Walk oldest -> newest so dispatched webhooks arrive in chronological order.
	for i := len(feed.Items) - 1; i >= 0; i-- {
		item := feed.Items[i]
		id := item.ID
		if id == "" {
			id = item.Link
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if !seed {
			newItems = append(newItems, item)
		}
	}
	n.mu.Unlock()

	if seed || len(newItems) == 0 {
		return
	}

	for _, item := range newItems {
		n.dispatch(ctx, channel, item)
	}
}

func (n *Notifier) dispatch(ctx context.Context, channel string, item app.FeedItemJSON) {
	body, err := json.Marshal(Payload{Channel: channel, Item: item})
	if err != nil {
		n.logger.Printf("notifier: marshal payload failed: %v", err)
		return
	}

	id := item.ID
	if id == "" {
		id = item.Link
	}
	for _, webhook := range n.cfg.Webhooks {
		if err := n.postWebhook(ctx, webhook, body); err != nil {
			n.logger.Printf("notifier: webhook %q failed: %v", webhook, err)
			continue
		}
		n.logger.Printf("notifier: webhook %q delivered channel=%q item=%q bytes=%d",
			webhook, channel, id, len(body))
	}
}

func (n *Notifier) postWebhook(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	res, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", res.StatusCode)
	}
	return nil
}
