package xapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// streamEndpointPath is the X filtered stream endpoint relative to BaseURL.
const (
	streamEndpointPath = "/tweets/search/stream"
	streamRulesPath    = "/tweets/search/stream/rules"
	// streamQuery selects the fields we need to build feed items. matching_rules
	// carries the tag we set per rule so we can route events to the right user
	// without extra lookups.
	streamQuery = "?expansions=author_id&tweet.fields=created_at&user.fields=username,description"
)

// bufferedTweet is a tweet captured from the live stream waiting to be flushed
// to subscribers on the next poll.
type bufferedTweet struct {
	ID        string
	Text      string
	CreatedAt time.Time
}

// userMeta caches X user profile info needed to build a feed envelope.
type userMeta struct {
	ID          string
	Username    string // canonical casing as returned by X
	Description string
}

// Stream opens a long-lived connection to X's filtered stream endpoint and
// buffers incoming tweets per user. Buffered events are drained by callers
// (typically the feed builder) and cleared after each drain, so memory stays
// bounded between polls.
type Stream struct {
	Client  *http.Client
	BaseURL string
	Token   string

	// Backoff controls reconnect delay bounds on network errors. Zero values
	// pick sane defaults in Start.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration

	// Logf, if set, receives human-readable diagnostic messages about stream
	// lifecycle (connects, reconnects, rule sync). Nil disables logging.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	buffers map[string][]bufferedTweet // canonical username (lower) -> tweets
	users   map[string]userMeta        // canonical username (lower) -> meta

	// started guards Start so it can only run once per Stream instance.
	started bool
}

// NewStream constructs a Stream ready to be Started. A nil client yields a
// default *http.Client without a read timeout (the stream is long-lived, so
// Client.Timeout would abort it prematurely).
func NewStream(token string, client *http.Client) *Stream {
	if client == nil {
		client = &http.Client{}
	}
	return &Stream{
		Client:  client,
		BaseURL: defaultBaseURL,
		Token:   token,
		buffers: make(map[string][]bufferedTweet),
		users:   make(map[string]userMeta),
	}
}

// Start validates usernames, installs stream rules for them, fetches and caches
// each user's canonical profile info, and spawns a background goroutine that
// keeps the stream connected until ctx is cancelled. It returns an error if
// the rule sync fails; stream read errors surface only in logs because the
// background loop reconnects.
func (s *Stream) Start(ctx context.Context, usernames []string) error {
	if strings.TrimSpace(s.Token) == "" {
		return fmt.Errorf("x.com bearer token is required")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("xapi stream already started")
	}
	s.started = true
	s.mu.Unlock()

	cleaned := make([]string, 0, len(usernames))
	for _, u := range usernames {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !usernameRE.MatchString(u) {
			return fmt.Errorf("invalid x.com username %q", u)
		}
		cleaned = append(cleaned, u)
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("xapi stream: at least one username is required")
	}

	// Prime metadata so feeds have a title/description even before the first
	// tweet arrives. Failures per user are non-fatal: we fall back to the raw
	// username casing.
	for _, u := range cleaned {
		meta := userMeta{Username: u}
		if loaded, err := s.fetchUser(ctx, u); err == nil {
			meta = loaded
		} else if s.Logf != nil {
			s.Logf("xapi stream: user lookup for %q failed: %v", u, err)
		}
		s.mu.Lock()
		s.users[strings.ToLower(u)] = meta
		s.mu.Unlock()
	}

	if err := s.syncRules(ctx, cleaned); err != nil {
		return fmt.Errorf("xapi stream: sync rules: %w", err)
	}

	initial := s.InitialBackoff
	if initial <= 0 {
		initial = time.Second
	}
	maxBackoff := s.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Minute
	}

	go s.runLoop(ctx, initial, maxBackoff)
	return nil
}

// Drain returns any tweets buffered for username since the last call and clears
// the buffer. The returned slice is owned by the caller.
func (s *Stream) Drain(username string) (userMeta, []bufferedTweet) {
	key := strings.ToLower(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.users[key]
	if !ok {
		meta = userMeta{Username: username}
	}
	items := s.buffers[key]
	delete(s.buffers, key)
	return meta, items
}

func (s *Stream) appendTweet(username string, t bufferedTweet) {
	key := strings.ToLower(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffers[key] = append(s.buffers[key], t)
}

// runLoop connects to the filtered stream and dispatches events. On any error
// or EOF it sleeps with exponential backoff (capped by maxBackoff) and
// reconnects until ctx is cancelled.
func (s *Stream) runLoop(ctx context.Context, initial, maxBackoff time.Duration) {
	backoff := initial
	for {
		if ctx.Err() != nil {
			return
		}
		err := s.consume(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && s.Logf != nil {
			s.Logf("xapi stream: disconnected: %v (retrying in %s)", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// consume opens one stream connection and reads until it closes. It returns
// the error (if any) that terminated the connection.
func (s *Stream) consume(ctx context.Context) error {
	endpoint := strings.TrimRight(s.BaseURL, "/") + streamEndpointPath + streamQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("stream connect failed with status %d", res.StatusCode)
	}

	if s.Logf != nil {
		s.Logf("xapi stream: connected")
	}

	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

type streamEvent struct {
	Data struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
		AuthorID  string `json:"author_id"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			Description string `json:"description"`
		} `json:"users"`
	} `json:"includes"`
	MatchingRules []struct {
		ID  string `json:"id"`
		Tag string `json:"tag"`
	} `json:"matching_rules"`
}

func (s *Stream) handleLine(line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		// X sends periodic empty keep-alive lines.
		return
	}
	var evt streamEvent
	if err := json.Unmarshal([]byte(trimmed), &evt); err != nil {
		if s.Logf != nil {
			s.Logf("xapi stream: decode event failed: %v", err)
		}
		return
	}
	if evt.Data.ID == "" {
		// Could be an error envelope or metadata we don't care about.
		return
	}

	// Refresh cached profile info from expansions so later feeds carry up-to-date metadata.
	for _, u := range evt.Includes.Users {
		if u.ID == "" || u.Username == "" {
			continue
		}
		s.mu.Lock()
		key := strings.ToLower(u.Username)
		if _, tracked := s.users[key]; tracked {
			s.users[key] = userMeta{ID: u.ID, Username: u.Username, Description: u.Description}
		}
		s.mu.Unlock()
	}

	created := time.Time{}
	if parsed, err := time.Parse(time.RFC3339, evt.Data.CreatedAt); err == nil {
		created = parsed
	}

	tweet := bufferedTweet{ID: evt.Data.ID, Text: evt.Data.Text, CreatedAt: created}

	// Prefer the matching rule tag because it carries the username we set when
	// installing the rule. Fall back to resolving the author via expansions.
	routed := false
	for _, rule := range evt.MatchingRules {
		if rule.Tag == "" {
			continue
		}
		s.appendTweet(rule.Tag, tweet)
		routed = true
	}
	if routed {
		return
	}
	for _, u := range evt.Includes.Users {
		if u.ID == evt.Data.AuthorID && u.Username != "" {
			s.appendTweet(u.Username, tweet)
			return
		}
	}
}

// fetchUser performs a one-off lookup to hydrate the metadata cache.
func (s *Stream) fetchUser(ctx context.Context, username string) (userMeta, error) {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/users/by/username/" + username + "?user.fields=description"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return userMeta{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	res, err := s.Client.Do(req)
	if err != nil {
		return userMeta{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return userMeta{}, fmt.Errorf("status %d", res.StatusCode)
	}
	var parsed userLookupResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return userMeta{}, err
	}
	if parsed.Data.Username == "" {
		return userMeta{}, fmt.Errorf("user not found")
	}
	return userMeta{
		ID:          parsed.Data.ID,
		Username:    parsed.Data.Username,
		Description: parsed.Data.Description,
	}, nil
}

// syncRules replaces the active filtered-stream rules with one rule per
// configured username so the stream emits only posts we care about. Each rule
// is tagged with the raw username for efficient fan-out in handleLine.
func (s *Stream) syncRules(ctx context.Context, usernames []string) error {
	existing, err := s.listRules(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		ids := make([]string, 0, len(existing))
		for _, r := range existing {
			ids = append(ids, r.ID)
		}
		if err := s.deleteRules(ctx, ids); err != nil {
			return err
		}
	}

	type addRule struct {
		Value string `json:"value"`
		Tag   string `json:"tag"`
	}
	rules := make([]addRule, 0, len(usernames))
	for _, u := range usernames {
		rules = append(rules, addRule{Value: "from:" + u, Tag: u})
	}
	body, err := json.Marshal(map[string]any{"add": rules})
	if err != nil {
		return err
	}
	return s.postRules(ctx, body)
}

type streamRule struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	Tag   string `json:"tag"`
}

func (s *Stream) listRules(ctx context.Context) ([]streamRule, error) {
	endpoint := strings.TrimRight(s.BaseURL, "/") + streamRulesPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list rules status %d", res.StatusCode)
	}
	var parsed struct {
		Data []streamRule `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

func (s *Stream) deleteRules(ctx context.Context, ids []string) error {
	body, err := json.Marshal(map[string]any{
		"delete": map[string]any{"ids": ids},
	})
	if err != nil {
		return err
	}
	return s.postRules(ctx, body)
}

func (s *Stream) postRules(ctx context.Context, body []byte) error {
	endpoint := strings.TrimRight(s.BaseURL, "/") + streamRulesPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("rules request status %d", res.StatusCode)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}
