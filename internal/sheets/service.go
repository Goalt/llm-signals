package sheets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://sheets.googleapis.com/v4/spreadsheets"
	timeoutSeconds = 30
)

type Service struct {
	Client        *http.Client
	BaseURL       string
	Authorization string
	DocumentID    string
	SheetName     string
}

type Row struct {
	ID        string
	Text      string
	Type      string
	Source    string
	Metadata  string
	Action    string
	CreatedAt string
	UpdatedAt string
}

func NewService(authorization, documentID, sheetName string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:        client,
		BaseURL:       defaultBaseURL,
		Authorization: authorization,
		DocumentID:    documentID,
		SheetName:     sheetName,
	}
}

func (s *Service) AppendRow(ctx context.Context, row Row) error {
	if err := s.validateConfigured(); err != nil {
		return err
	}
	payload := map[string]any{
		"majorDimension": "ROWS",
		"values": [][]string{{
			row.ID,
			row.Text,
			row.Type,
			row.Source,
			row.Metadata,
			row.Action,
			row.CreatedAt,
			row.UpdatedAt,
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := s.valuesURL(url.PathEscape(s.SheetName+"!A:H")) + ":append?valueInputOption=RAW&insertDataOption=INSERT_ROWS"
	return s.doJSON(ctx, http.MethodPost, endpoint, body, nil)
}

func (s *Service) UpdateAction(ctx context.Context, id, action string) error {
	if err := s.validateConfigured(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("sheet row ID is required")
	}

	values, err := s.fetchValues(ctx, s.SheetName+"!A:H")
	if err != nil {
		return err
	}
	rowNumber := 0
	for i, row := range values {
		if len(row) > 0 && row[0] == id {
			rowNumber = i + 1
			break
		}
	}
	if rowNumber == 0 {
		return fmt.Errorf("sheet row with id %q not found", id)
	}

	payload := map[string]any{
		"majorDimension": "ROWS",
		"values":         [][]string{{action}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rangePath := url.PathEscape(fmt.Sprintf("%s!F%d", s.SheetName, rowNumber))
	endpoint := s.valuesURL(rangePath) + "?valueInputOption=RAW"
	return s.doJSON(ctx, http.MethodPut, endpoint, body, nil)
}

func (s *Service) CheckAccess(ctx context.Context) error {
	if err := s.validateConfigured(); err != nil {
		return err
	}
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/" + url.PathEscape(s.DocumentID)
	return s.doJSON(ctx, http.MethodGet, endpoint, nil, nil)
}

func (s *Service) fetchValues(ctx context.Context, valueRange string) ([][]string, error) {
	var parsed struct {
		Values [][]string `json:"values"`
	}
	endpoint := s.valuesURL(url.PathEscape(valueRange))
	if err := s.doJSON(ctx, http.MethodGet, endpoint, nil, &parsed); err != nil {
		return nil, err
	}
	return parsed.Values, nil
}

func (s *Service) doJSON(ctx context.Context, method, endpoint string, body []byte, out any) error {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", s.Authorization)
	req.Header.Set("User-Agent", "tg-channel-to-rss")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("google sheets request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (s *Service) valuesURL(valueRange string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/" + url.PathEscape(s.DocumentID) + "/values/" + valueRange
}

func (s *Service) validateConfigured() error {
	if strings.TrimSpace(s.Authorization) == "" {
		return fmt.Errorf("google sheets authorization is required")
	}
	if strings.TrimSpace(s.DocumentID) == "" {
		return fmt.Errorf("google sheets document ID is required")
	}
	if strings.TrimSpace(s.SheetName) == "" {
		return fmt.Errorf("google sheets sheet name is required")
	}
	return nil
}
