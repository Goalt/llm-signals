package xapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

const (
	defaultBaseURL = "https://api.x.com/2"
	timeoutSeconds = 30
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

type Service struct {
	Client  *http.Client
	BaseURL string
	Token   string
	Now     func() time.Time
	// UseFilteredStream switches tweet reads from user timeline polling to
	// X API filtered stream consumption.
	UseFilteredStream bool
	// Limiter throttles outgoing HTTP requests. Nil disables throttling.
	Limiter *RateLimiter

	streamMu sync.Mutex
}

func NewService(token string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:  client,
		BaseURL: defaultBaseURL,
		Token:   token,
		Now:     time.Now,
	}
}

func (s *Service) GetJSONFeed(username string) (string, error) {
	if strings.TrimSpace(s.Token) == "" {
		return "", fmt.Errorf("x.com bearer token is required")
	}
	if !usernameRE.MatchString(username) {
		return "", fmt.Errorf("invalid x.com username")
	}

	user, err := s.getUser(username)
	if err != nil {
		return "", err
	}

	var tweets *tweetsResponse
	if s.UseFilteredStream {
		tweets, err = s.getTweetsFromFilteredStream(user.Data.Username)
	} else {
		tweets, err = s.getTweets(user.Data.ID)
	}
	if err != nil {
		return "", err
	}

	feed := app.FeedJSON{
		Title:       "@" + user.Data.Username,
		Link:        "https://x.com/" + user.Data.Username,
		Description: user.Data.Description,
		Created:     s.Now(),
		Items:       make([]app.FeedItemJSON, 0, len(tweets.Data)),
	}

	for _, tweet := range tweets.Data {
		createdAt := s.Now()
		if parsed, err := time.Parse(time.RFC3339, tweet.CreatedAt); err == nil {
			createdAt = parsed
		}

		tweetLink := "https://x.com/" + user.Data.Username + "/status/" + tweet.ID
		escaped := html.EscapeString(tweet.Text)
		feed.Items = append(feed.Items, app.FeedItemJSON{
			Title:       "New post from @" + user.Data.Username,
			Description: "<p>" + escaped + "</p>",
			Link:        tweetLink,
			Created:     createdAt,
			ID:          tweet.ID,
			Content:     escaped,
		})
	}

	out, err := json.Marshal(feed)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type userLookupResponse struct {
	Data struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Username    string `json:"username"`
		Description string `json:"description"`
	} `json:"data"`
}

type tweetsResponse struct {
	Data []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
}

type streamRulesResponse struct {
	Data []struct {
		ID    string `json:"id"`
		Value string `json:"value"`
		Tag   string `json:"tag"`
	} `json:"data"`
}

type streamEventResponse struct {
	Data struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
		AuthorID  string `json:"author_id"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	} `json:"includes"`
}

func (s *Service) getUser(username string) (*userLookupResponse, error) {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/users/by/username/" + url.PathEscape(username) + "?user.fields=description"
	var parsed userLookupResponse
	if err := s.getJSON(endpoint, &parsed); err != nil {
		return nil, err
	}
	if parsed.Data.ID == "" || parsed.Data.Username == "" {
		return nil, fmt.Errorf("x.com user not found")
	}
	return &parsed, nil
}

func (s *Service) getTweets(userID string) (*tweetsResponse, error) {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/users/" + url.PathEscape(userID) + "/tweets?max_results=10&tweet.fields=created_at"
	var parsed tweetsResponse
	if err := s.getJSON(endpoint, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Service) getTweetsFromFilteredStream(username string) (*tweetsResponse, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()

	if err := s.ensureStreamRule(username); err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(s.BaseURL, "/") + "/tweets/search/stream?tweet.fields=created_at,author_id&expansions=author_id&user.fields=username"
	streamTimeout := s.Client.Timeout
	if streamTimeout <= 0 {
		streamTimeout = timeoutSeconds * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	if s.Limiter != nil {
		s.Limiter.Wait()
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("x.com API request failed with status %d", res.StatusCode)
	}

	items := make([]struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
	}, 0)
	if err := s.consumeStream(res.Body, username, &items); err != nil {
		return nil, err
	}
	return &tweetsResponse{Data: items}, nil
}

func (s *Service) ensureStreamRule(username string) error {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/tweets/search/stream/rules"

	var rules streamRulesResponse
	if err := s.getJSON(endpoint, &rules); err != nil {
		return err
	}

	expected := "from:" + username
	for _, rule := range rules.Data {
		if strings.TrimSpace(rule.Value) == expected {
			return nil
		}
	}

	payload := map[string]any{
		"add": []map[string]string{
			{
				"value": expected,
				"tag":   "tg-channel-to-rss:" + username,
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")
	req.Header.Set("Content-Type", "application/json")

	if s.Limiter != nil {
		s.Limiter.Wait()
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return fmt.Errorf("x.com API request failed with status %d", res.StatusCode)
	}
	return nil
}

func (s *Service) consumeStream(body io.Reader, username string, out *[]struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}) error {
	scanner := bufio.NewScanner(body)
	const streamPrefix = "data:"
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, streamPrefix) {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, streamPrefix))
		if payload == "" {
			continue
		}

		var event streamEventResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Data.ID == "" {
			continue
		}

		matched := false
		for _, user := range event.Includes.Users {
			if strings.EqualFold(user.Username, username) && user.ID == event.Data.AuthorID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		*out = append(*out, struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			CreatedAt string `json:"created_at"`
		}{
			ID:        event.Data.ID,
			Text:      event.Data.Text,
			CreatedAt: event.Data.CreatedAt,
		})
	}

	if err := scanner.Err(); err != nil && !isTimeoutErr(err) {
		return err
	}
	return nil
}

func isTimeoutErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "Client.Timeout exceeded")
}

func (s *Service) getJSON(endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	if s.Limiter != nil {
		s.Limiter.Wait()
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("x.com API request failed with status %d", res.StatusCode)
	}

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return err
	}
	return nil
}
