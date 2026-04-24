package polymarket

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/xapi"
)

const (
	defaultBaseURL      = "https://clob.polymarket.com"
	defaultFeedEndpoint = "/sampling-markets"
	timeoutSeconds      = 30
)

type Service struct {
	Client        *http.Client
	BaseURL       string
	Authorization string
	Now           func() time.Time
	// Limiter throttles outgoing HTTP requests. Nil disables throttling.
	Limiter *xapi.RateLimiter
}

func NewService(authorization string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:        client,
		BaseURL:       defaultBaseURL,
		Authorization: authorization,
		Now:           time.Now,
	}
}

func (s *Service) GetJSONFeed(channel string) (string, error) {
	endpoint, sourceLink, title, err := s.resolveEndpoint(channel)
	if err != nil {
		return "", err
	}

	var parsed marketListResponse
	if err := s.getJSON(endpoint, &parsed); err != nil {
		return "", err
	}

	items := make([]app.FeedItemJSON, 0, len(parsed.Data))
	for _, market := range parsed.Data {
		id := strings.TrimSpace(market.ConditionID)
		if id == "" {
			id = strings.TrimSpace(market.QuestionID)
		}
		if id == "" {
			id = strings.TrimSpace(market.MarketSlug)
		}
		if id == "" {
			continue
		}

		itemLink := sourceLink
		if slug := strings.TrimSpace(market.MarketSlug); slug != "" {
			itemLink = "https://polymarket.com/event/" + slug
		}

		question := strings.TrimSpace(market.Question)
		if question == "" {
			question = "New Polymarket event"
		}

		content := strings.TrimSpace(market.Description)
		if content == "" {
			content = question
		}
		safeContent := html.EscapeString(content)
		if safeContent != "" {
			safeContent = "<p>" + safeContent + "</p>"
		}

		items = append(items, app.FeedItemJSON{
			Title:       question,
			Description: safeContent,
			Link:        itemLink,
			Created:     s.marketTime(market),
			ID:          id,
			Content:     safeContent,
			Metadata:    marketMetadata(market, itemLink),
		})
	}

	feed := app.FeedJSON{
		Title:       title,
		Link:        sourceLink,
		Description: "Polymarket events feed",
		Created:     s.Now(),
		Items:       items,
	}

	out, err := json.Marshal(feed)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type marketListResponse struct {
	Data []market `json:"data"`
}

type market struct {
	ConditionID             string `json:"condition_id"`
	QuestionID              string `json:"question_id"`
	MarketSlug              string `json:"market_slug"`
	Question                string `json:"question"`
	Description             string `json:"description"`
	EndDateISO              string `json:"end_date_iso"`
	AcceptingOrderTimestamp string `json:"accepting_order_timestamp"`
}

// marketMetadata returns the raw upstream market fields as a generic map so
// webhook consumers receive the maximum data available from Polymarket.
func marketMetadata(m market, itemLink string) map[string]any {
	md := map[string]any{}
	if m.ConditionID != "" {
		md["condition_id"] = m.ConditionID
	}
	if m.QuestionID != "" {
		md["question_id"] = m.QuestionID
	}
	if m.MarketSlug != "" {
		md["market_slug"] = m.MarketSlug
	}
	if m.Question != "" {
		md["question"] = m.Question
	}
	if m.Description != "" {
		md["description"] = m.Description
	}
	if m.EndDateISO != "" {
		md["end_date_iso"] = m.EndDateISO
	}
	if m.AcceptingOrderTimestamp != "" {
		md["accepting_order_timestamp"] = m.AcceptingOrderTimestamp
	}
	if itemLink != "" {
		md["event_url"] = itemLink
	}
	return md
}

func (s *Service) resolveEndpoint(channel string) (string, string, string, error) {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		return "", "", "", fmt.Errorf("invalid polymarket endpoint")
	}

	pathWithQuery := ch
	switch ch {
	case "sampling-markets", "events":
		pathWithQuery = defaultFeedEndpoint
	case "markets":
		pathWithQuery = "/markets"
	default:
		if !strings.HasPrefix(pathWithQuery, "/") {
			pathWithQuery = "/" + pathWithQuery
		}
	}

	base, err := url.Parse(strings.TrimRight(s.BaseURL, "/"))
	if err != nil {
		return "", "", "", fmt.Errorf("invalid polymarket base URL: %w", err)
	}
	rel, err := url.Parse(pathWithQuery)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid polymarket endpoint %q: %w", channel, err)
	}
	full := base.ResolveReference(rel)
	return full.String(), full.String(), "Polymarket " + ch, nil
}

func (s *Service) marketTime(m market) time.Time {
	for _, candidate := range []string{m.AcceptingOrderTimestamp, m.EndDateISO} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			return parsed
		}
	}
	return s.Now()
}

func (s *Service) getJSON(endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "tg-channel-to-rss")
	if strings.TrimSpace(s.Authorization) != "" {
		req.Header.Set("Authorization", s.Authorization)
	}

	if s.Limiter != nil {
		s.Limiter.Wait()
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("polymarket API request failed with status %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return err
	}
	return nil
}
