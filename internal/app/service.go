package app

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	FeedPathPrefix = "/feed/"
	defaultBaseURL = "https://t.me"
	timeoutSeconds = 30
)

var (
	urlRE         = regexp.MustCompile(`(https?://[^\s<>"']+)`)
	channelNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)
	bgURLRE       = regexp.MustCompile(`background-image:\s*url\(['\"]?(?P<u>[^'\")]+)['\"]?\)`)
)

type Service struct {
	Client  *http.Client
	BaseURL string
	Now     func() time.Time
}

type FeedJSON struct {
	Title       string         `json:"title"`
	Link        string         `json:"link"`
	Description string         `json:"description"`
	Created     time.Time      `json:"created"`
	Items       []FeedItemJSON `json:"items"`
}

type FeedItemJSON struct {
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Link        string             `json:"link"`
	Created     time.Time          `json:"created"`
	ID          string             `json:"id"`
	Content     string             `json:"content"`
	Enclosure   *FeedEnclosureJSON `json:"enclosure,omitempty"`
}

type FeedEnclosureJSON struct {
	URL    string `json:"url"`
	Length string `json:"length"`
	Type   string `json:"type"`
}

func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{Client: client, BaseURL: defaultBaseURL, Now: time.Now}
}

func (s *Service) HandleFeedRequest(channelName string) (int, string, map[string]string) {
	headers := map[string]string{"Content-Type": "text/plain; charset=UTF-8"}

	if strings.TrimSpace(channelName) == "" {
		return http.StatusBadRequest, "Missing channel_name", headers
	}
	if !channelNameRE.MatchString(channelName) {
		return http.StatusBadRequest, "Invalid channel_name", headers
	}

	jsonBody, err := s.GetJSONFeed(channelName)
	if err != nil {
		return http.StatusBadRequest, err.Error(), headers
	}

	return http.StatusOK, jsonBody, map[string]string{
		"Content-Type":  "application/json; charset=UTF-8",
		"Cache-Control": "max-age=60, public",
	}
}

func (s *Service) GetJSONFeed(channelName string) (string, error) {
	doc, err := s.getDoc(channelName)
	if err != nil {
		return "", err
	}

	title := channelName
	if doc.Find("title").First().Length() > 0 {
		if t := strings.TrimSpace(doc.Find("title").First().Text()); t != "" {
			title = t
		}
	}

	description := "Posts from " + title
	if og, ok := doc.Find("meta[property='og:description']").First().Attr("content"); ok {
		if d := strings.TrimSpace(og); d != "" {
			description = d
		}
	}

	feed := FeedJSON{
		Title:       title,
		Link:        s.channelURL(channelName),
		Description: description,
		Created:     s.Now(),
	}

	items := make([]FeedItemJSON, 0)
	doc.Find("div.tgme_widget_message_bubble").Each(func(_ int, sel *goquery.Selection) {
		item := buildItem(sel, channelName)
		if item != nil {
			items = append(items, *item)
		}
	})
	feed.Items = items

	jsonBytes, err := json.Marshal(feed)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}

func (s *Service) channelURL(channelName string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/s/" + channelName
}

func (s *Service) getDoc(channelName string) (*goquery.Document, error) {
	url := s.channelURL(channelName)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Telegram channel not found")
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func buildItem(bubble *goquery.Selection, channelName string) *FeedItemJSON {
	link, ok := bubble.Find("a.tgme_widget_message_date[href]").First().Attr("href")
	if !ok || strings.TrimSpace(link) == "" {
		return nil
	}
	link = strings.Replace(link, "://t.me/", "://t.me/s/", 1)

	pub := time.Now()
	if rawPub, ok := bubble.Find("time.time[datetime]").First().Attr("datetime"); ok {
		if parsed, err := time.Parse(time.RFC3339, rawPub); err == nil {
			pub = parsed
		}
	}

	msgText := bubble.Find("div.tgme_widget_message_text").First()
	rawHTML, _ := msgText.Html()
	rawHTML = absolutizeLinks(strings.TrimSpace(rawHTML), defaultBaseURL+"/")

	plainText := strings.TrimSpace(msgText.Text())
	descriptionHTML := "<p>" + autolinkPlain(plainText) + "</p>"

	photos := getPhotoAssets(bubble)
	mediaHTML := ""
	for _, photo := range photos {
		mediaHTML += `<p><img src="` + escapeAttr(photo) + `" referrerpolicy="no-referrer"/></p>`
	}

	item := &FeedItemJSON{
		Title:       "New post in channel @" + channelName,
		Description: descriptionHTML + mediaHTML,
		Link:        link,
		Created:     pub,
		ID:          link,
		Content:     strings.TrimSpace(rawHTML + mediaHTML),
	}

	if len(photos) > 0 {
		item.Enclosure = &FeedEnclosureJSON{URL: photos[0], Length: "0", Type: guessMIME(photos[0])}
	}
	return item
}

func autolinkPlain(text string) string {
	if text == "" {
		return ""
	}

	matches := urlRE.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return html.EscapeString(text)
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(html.EscapeString(text[last:m[0]]))
		url := text[m[0]:m[1]]
		safeURL := html.EscapeString(url)
		out.WriteString(`<a href="` + safeURL + `" rel="noopener" target="_blank">` + safeURL + `</a>`)
		last = m[1]
	}
	out.WriteString(html.EscapeString(text[last:]))
	return out.String()
}

func getPhotoAssets(bubble *goquery.Selection) []string {
	photos := make([]string, 0)

	bubble.Find("*[style]").Each(func(_ int, sel *goquery.Selection) {
		style, ok := sel.Attr("style")
		if !ok {
			return
		}
		if isReactionOrEmoji(sel) {
			return
		}
		m := bgURLRE.FindStringSubmatch(style)
		if len(m) >= 2 {
			photos = append(photos, m[1])
		}
	})

	bubble.Find("a.tgme_widget_message_link_preview img[src]").Each(func(_ int, img *goquery.Selection) {
		if isReactionOrEmoji(img) {
			return
		}
		if src, ok := img.Attr("src"); ok {
			photos = append(photos, src)
		}
	})

	bubble.Find("img[src]").Each(func(_ int, img *goquery.Selection) {
		if isReactionOrEmoji(img) {
			return
		}
		if src, ok := img.Attr("src"); ok {
			photos = append(photos, src)
		}
	})

	seen := map[string]struct{}{}
	uniq := make([]string, 0, len(photos))
	for _, p := range photos {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		uniq = append(uniq, p)
	}
	return uniq
}

func isReactionOrEmoji(sel *goquery.Selection) bool {
	for current := sel; current.Length() > 0; current = current.Parent() {
		classes, _ := current.Attr("class")
		if strings.Contains(classes, "tgme_widget_message_reactions") || strings.Contains(classes, "tgme_widget_message_reactions_small") {
			return true
		}
	}

	classes, _ := sel.Attr("class")
	if strings.Contains(classes, "emoji") || strings.Contains(classes, "tgme_widget_emoji") || strings.Contains(classes, "emoji_image") {
		return true
	}

	src, _ := sel.Attr("src")
	if strings.Contains(src, "/emoji/") || strings.Contains(src, "/stickers/") || strings.Contains(src, "emoji-static") || strings.Contains(src, "emoji-animated") {
		return true
	}

	style, _ := sel.Attr("style")
	if strings.Contains(style, "emoji") || strings.Contains(style, "sticker") {
		return true
	}

	return false
}

func absolutizeLinks(rawHTML, base string) string {
	replacements := []struct {
		re   *regexp.Regexp
		attr string
	}{
		{regexp.MustCompile(`href="(/[^\"]+)"`), `href="`},
		{regexp.MustCompile(`href='(/[^']+)'`), `href='`},
		{regexp.MustCompile(`src="(/[^\"]+)"`), `src="`},
		{regexp.MustCompile(`src='(/[^']+)'`), `src='`},
	}

	result := rawHTML
	for _, replacement := range replacements {
		result = replacement.re.ReplaceAllStringFunc(result, func(match string) string {
			sub := replacement.re.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			joined := strings.TrimRight(base, "/") + sub[1]
			if strings.HasSuffix(replacement.attr, "\"") {
				return replacement.attr + joined + `"`
			}
			return replacement.attr + joined + `'`
		})
	}
	return result
}

func guessMIME(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

func escapeAttr(value string) string {
	return strings.ReplaceAll(value, `"`, "&quot;")
}
