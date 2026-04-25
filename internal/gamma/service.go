package gamma

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://gamma-api.polymarket.com"
	timeoutSeconds = 30
)

type Service struct {
	Client  *http.Client
	BaseURL string
}

func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:  client,
		BaseURL: defaultBaseURL,
	}
}

func (s *Service) EventsByTag(ctx context.Context, tag string) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.BaseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid gamma base URL: %w", err)
	}
	ref, err := url.Parse("/events/keyset")
	if err != nil {
		return "", err
	}
	endpoint := base.ResolveReference(ref)
	q := endpoint.Query()
	q.Set("limit", "20")
	q.Set("ascending", "true")
	if cleanTag := strings.TrimSpace(tag); cleanTag != "" {
		q.Set("tag_slug", cleanTag)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	res, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gamma API request failed with status %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", fmt.Errorf("gamma API returned empty body")
	}
	return trimmed, nil
}

func (s *Service) CheckAccess(ctx context.Context) error {
	_, err := s.EventsByTag(ctx, "")
	return err
}
