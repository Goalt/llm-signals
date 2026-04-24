package xapi

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
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
	// Limiter throttles outgoing HTTP requests. Nil disables throttling.
	Limiter *RateLimiter
	// Stream, when set, makes GetJSONFeed return tweets accumulated from the
	// live filtered stream since the previous call instead of performing
	// outbound HTTP requests. The buffer is cleared after each drain.
	Stream *Stream
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

	if s.Stream != nil {
		return s.buildFeedFromStream(username)
	}

	user, err := s.getUser(username)
	if err != nil {
		return "", err
	}

	tweets, err := s.getTweets(user.Data.ID)
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

// buildFeedFromStream assembles a feed from tweets the Stream has buffered
// since the last call. After reading, the buffer is cleared so memory usage
// stays bounded between polls.
func (s *Service) buildFeedFromStream(username string) (string, error) {
	meta, tweets := s.Stream.Drain(username)
	canonical := meta.Username
	if canonical == "" {
		canonical = username
	}
	feed := app.FeedJSON{
		Title:       "@" + canonical,
		Link:        "https://x.com/" + canonical,
		Description: meta.Description,
		Created:     s.Now(),
		Items:       make([]app.FeedItemJSON, 0, len(tweets)),
	}
	for _, t := range tweets {
		created := t.CreatedAt
		if created.IsZero() {
			created = s.Now()
		}
		escaped := html.EscapeString(t.Text)
		feed.Items = append(feed.Items, app.FeedItemJSON{
			Title:       "New post from @" + canonical,
			Description: "<p>" + escaped + "</p>",
			Link:        "https://x.com/" + canonical + "/status/" + t.ID,
			Created:     created,
			ID:          t.ID,
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

func (s *Service) getJSON(endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	s.Limiter.Wait()

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
