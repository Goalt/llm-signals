package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	timeoutSeconds = 30
)

type Service struct {
	Client   *http.Client
	BaseURL  string
	BotToken string
	ChatID   string
}

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func NewService(botToken, chatID string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:   client,
		BaseURL:  defaultBaseURL,
		BotToken: botToken,
		ChatID:   chatID,
	}
}

func (s *Service) Send(ctx context.Context, text string) error {
	if strings.TrimSpace(s.BotToken) == "" {
		return fmt.Errorf("telegram bot token is required")
	}
	if strings.TrimSpace(s.ChatID) == "" {
		return fmt.Errorf("telegram chat ID is required")
	}
	body, err := json.Marshal(map[string]string{
		"chat_id": s.ChatID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	return s.doJSON(ctx, http.MethodPost, s.methodURL("sendMessage"), body)
}

func (s *Service) CheckAccess(ctx context.Context) error {
	return s.doJSON(ctx, http.MethodGet, s.methodURL("getMe"), nil)
}

func (s *Service) doJSON(ctx context.Context, method, endpoint string, body []byte) error {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("telegram API request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(payload)))
	}

	var parsed telegramResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return err
	}
	if !parsed.OK {
		if parsed.Description == "" {
			parsed.Description = "telegram API returned ok=false"
		}
		return errors.New(parsed.Description)
	}
	return nil
}

func (s *Service) methodURL(method string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/bot" + url.PathEscape(s.BotToken) + "/" + method
}
