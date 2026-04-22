// Package rssapi fetches standard RSS 2.0 and Atom feeds and converts them to
// the internal app.FeedJSON representation so the notifier can reuse the same
// dispatch pipeline as the Telegram and x.com pollers.
package rssapi

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
)

const (
	defaultTimeoutSeconds = 30
	// maxBodyBytes caps the amount of data read from an upstream feed to guard
	// against accidentally huge payloads. 10 MiB is comfortably above any
	// reasonable RSS/Atom feed.
	maxBodyBytes = 10 * 1024 * 1024
)

// Service fetches a remote RSS/Atom feed and returns it as a JSON string in
// the shape expected by notifier.FeedFetcher.
type Service struct {
	Client *http.Client
	Now    func() time.Time
	// Limiter optionally throttles outgoing HTTP requests. A nil limiter
	// disables throttling; the interface-typed field lets callers reuse the
	// shared xapi.RateLimiter without creating an import cycle.
	Limiter Limiter
}

// Limiter is the minimal interface Service needs from a rate limiter. It is
// satisfied by *xapi.RateLimiter.
type Limiter interface {
	Wait()
}

// NewService constructs an RSS Service. When client is nil a default client
// with a conservative timeout is used.
func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeoutSeconds * time.Second}
	}
	return &Service{
		Client: client,
		Now:    time.Now,
	}
}

// GetJSONFeed fetches the feed at feedURL, parses it as RSS 2.0 or Atom, and
// returns a marshaled app.FeedJSON string.
//
// The method name intentionally matches notifier.FeedFetcher so the RSS
// service is a drop-in fetcher for the existing notifier.
func (s *Service) GetJSONFeed(feedURL string) (string, error) {
	if err := validateFeedURL(feedURL); err != nil {
		return "", err
	}

	body, err := s.fetch(feedURL)
	if err != nil {
		return "", err
	}

	feed, err := parseFeed(body, feedURL, s.now())
	if err != nil {
		return "", err
	}

	out, err := json.Marshal(feed)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validateFeedURL(feedURL string) error {
	if strings.TrimSpace(feedURL) == "" {
		return fmt.Errorf("rss feed url is required")
	}
	u, err := url.Parse(feedURL)
	if err != nil {
		return fmt.Errorf("invalid rss feed url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid rss feed url scheme: %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid rss feed url: missing host")
	}
	return nil
}

func (s *Service) fetch(feedURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	if s.Limiter != nil {
		s.Limiter.Wait()
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("rss feed request failed with status %d", res.StatusCode)
	}

	return io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))
}

// --- parsing ---------------------------------------------------------------

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	PubDate     string    `xml:"pubDate"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	Description string        `xml:"description"`
	PubDate     string        `xml:"pubDate"`
	GUID        string        `xml:"guid"`
	Enclosure   *rssEnclosure `xml:"enclosure"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type atomFeed struct {
	XMLName  xml.Name    `xml:"feed"`
	Title    string      `xml:"title"`
	Links    []atomLink  `xml:"link"`
	Updated  string      `xml:"updated"`
	Subtitle string      `xml:"subtitle"`
	Entries  []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
}

// parseFeed dispatches to the RSS or Atom parser based on the XML root
// element. feedURL is used as a fallback for the feed Link when the upstream
// does not advertise one.
func parseFeed(body []byte, feedURL string, now time.Time) (app.FeedJSON, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return app.FeedJSON{}, fmt.Errorf("rss feed is empty")
	}

	switch detectRoot(body) {
	case "rss":
		return parseRSS(body, feedURL, now)
	case "feed":
		return parseAtom(body, feedURL, now)
	default:
		return app.FeedJSON{}, fmt.Errorf("unsupported feed format")
	}
}

// detectRoot returns the XML root element's local name or "" when it can't be
// determined. It uses xml.Decoder so XML prologs, comments, and whitespace are
// handled correctly.
func detectRoot(body []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

func parseRSS(body []byte, feedURL string, now time.Time) (app.FeedJSON, error) {
	var root rssRoot
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	if err := dec.Decode(&root); err != nil {
		return app.FeedJSON{}, fmt.Errorf("parse rss: %w", err)
	}

	feed := app.FeedJSON{
		Title:       strings.TrimSpace(root.Channel.Title),
		Link:        firstNonEmpty(strings.TrimSpace(root.Channel.Link), feedURL),
		Description: strings.TrimSpace(root.Channel.Description),
		Created:     parseTimeOr(root.Channel.PubDate, now),
		Items:       make([]app.FeedItemJSON, 0, len(root.Channel.Items)),
	}

	for _, item := range root.Channel.Items {
		link := strings.TrimSpace(item.Link)
		id := strings.TrimSpace(item.GUID)
		if id == "" {
			id = link
		}
		if id == "" {
			// Skip items without any stable identifier — the notifier relies
			// on it to deduplicate deliveries.
			continue
		}

		fi := app.FeedItemJSON{
			Title:       strings.TrimSpace(item.Title),
			Description: item.Description,
			Link:        link,
			Created:     parseTimeOr(item.PubDate, now),
			ID:          id,
			Content:     item.Description,
		}
		if item.Enclosure != nil && strings.TrimSpace(item.Enclosure.URL) != "" {
			fi.Enclosure = &app.FeedEnclosureJSON{
				URL:    item.Enclosure.URL,
				Length: item.Enclosure.Length,
				Type:   item.Enclosure.Type,
			}
		}
		feed.Items = append(feed.Items, fi)
	}
	return feed, nil
}

func parseAtom(body []byte, feedURL string, now time.Time) (app.FeedJSON, error) {
	var root atomFeed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	if err := dec.Decode(&root); err != nil {
		return app.FeedJSON{}, fmt.Errorf("parse atom: %w", err)
	}

	feed := app.FeedJSON{
		Title:       strings.TrimSpace(root.Title),
		Link:        firstNonEmpty(pickAtomLink(root.Links), feedURL),
		Description: strings.TrimSpace(root.Subtitle),
		Created:     parseTimeOr(root.Updated, now),
		Items:       make([]app.FeedItemJSON, 0, len(root.Entries)),
	}

	for _, entry := range root.Entries {
		link := pickAtomLink(entry.Links)
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = link
		}
		if id == "" {
			continue
		}

		created := parseTimeOr(entry.Published, time.Time{})
		if created.IsZero() {
			created = parseTimeOr(entry.Updated, now)
		}

		content := entry.Content
		if strings.TrimSpace(content) == "" {
			content = entry.Summary
		}

		feed.Items = append(feed.Items, app.FeedItemJSON{
			Title:       strings.TrimSpace(entry.Title),
			Description: strings.TrimSpace(entry.Summary),
			Link:        link,
			Created:     created,
			ID:          id,
			Content:     content,
		})
	}
	return feed, nil
}

// pickAtomLink chooses the most appropriate link href for an Atom element.
// Preference: rel="alternate" (or empty rel) with a non-empty href, else the
// first link with any href.
func pickAtomLink(links []atomLink) string {
	for _, l := range links {
		href := strings.TrimSpace(l.Href)
		if href == "" {
			continue
		}
		if l.Rel == "" || l.Rel == "alternate" {
			return href
		}
	}
	for _, l := range links {
		if href := strings.TrimSpace(l.Href); href != "" {
			return href
		}
	}
	return ""
}

// parseTimeOr parses a date string in common RSS/Atom formats and returns
// fallback if parsing fails or the input is empty.
func parseTimeOr(s string, fallback time.Time) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05 -0700",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
