package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	timeoutSeconds = 30
)

type PolymarketTool interface {
	EventsByTag(ctx context.Context, tag string) (string, error)
}

type Service struct {
	Client        *http.Client
	BaseURL       string
	Authorization string
	Model         string
	Polymarket    PolymarketTool
}

type AnalyzeInput struct {
	ID       string         `json:"id"`
	Source   string         `json:"source"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type AnalysisResult struct {
	Title       string `json:"title"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Action      string `json:"action"`
	Explanation string `json:"explanation"`
	Tags        string `json:"tags"`
}

type chatCompletionRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []toolDef     `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type toolDef struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func NewService(authorization, model string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: timeoutSeconds * time.Second}
	}
	return &Service{
		Client:        client,
		BaseURL:       defaultBaseURL,
		Authorization: authorization,
		Model:         model,
	}
}

func (s *Service) AnalyzeNews(ctx context.Context, input AnalyzeInput) (AnalysisResult, error) {
	if strings.TrimSpace(s.Authorization) == "" {
		return AnalysisResult{}, fmt.Errorf("openrouter authorization is required")
	}
	if strings.TrimSpace(s.Model) == "" {
		return AnalysisResult{}, fmt.Errorf("openrouter model is required")
	}

	metadataJSON := "{}"
	if len(input.Metadata) > 0 {
		if b, err := json.Marshal(input.Metadata); err == nil {
			metadataJSON = string(b)
		}
	}

	messages := []chatMessage{
		{
			Role: "system",
			Content: strings.TrimSpace(`Ты помощник по торговле на основании новостей для Polymarket.
Отвечай полностью на русском.
Верни только JSON-объект без markdown и без пояснений вне JSON.
Поля результата: title, id, source, action, explanation, tags.
Если действие не нужно делать, в поле action напиши "Игнорировать новость".
Тэги должны быть строкой из односложных тегов через пробел, каждый начинается с #.
Объяснение действия должно быть простым, 2-3 предложения.`),
		},
		{
			Role: "user",
			Content: fmt.Sprintf("Новость: %s\n\nmetadata: %s\n\nid новости: %s\nsource: %s",
				input.Text, metadataJSON, input.ID, input.Source),
		},
	}

	tools := []toolDef{}
	if s.Polymarket != nil {
		tools = append(tools, toolDef{
			Type: "function",
			Function: toolFunction{
				Name:        "get_polymarket_events",
				Description: "Get a list of Polymarket events by optional tag slug.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tag": map[string]any{
							"type":        "string",
							"description": "Tag slug for Polymarket event search",
						},
					},
				},
			},
		})
	}

	for i := 0; i < 4; i++ {
		resp, err := s.complete(ctx, chatCompletionRequest{
			Model:      s.Model,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: "auto",
		})
		if err != nil {
			return AnalysisResult{}, err
		}
		if len(resp.Choices) == 0 {
			return AnalysisResult{}, fmt.Errorf("openrouter returned no choices")
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) > 0 {
			messages = append(messages, chatMessage{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})
			for _, call := range msg.ToolCalls {
				result, toolErr := s.runTool(ctx, call)
				if toolErr != nil {
					result = fmt.Sprintf(`{"error":%q}`, toolErr.Error())
				}
				messages = append(messages, chatMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Content:    result,
				})
			}
			continue
		}
		analysis, err := parseAnalysis(msg.Content)
		if err != nil {
			return AnalysisResult{}, err
		}
		if analysis.ID == "" {
			analysis.ID = input.ID
		}
		if analysis.Source == "" {
			analysis.Source = input.Source
		}
		if analysis.Title == "" {
			analysis.Title = fallbackTitle(input.Text)
		}
		if analysis.Action == "" {
			analysis.Action = "Игнорировать новость"
		}
		return analysis, nil
	}

	return AnalysisResult{}, fmt.Errorf("openrouter exceeded tool-call iterations")
}

func (s *Service) CheckAccess(ctx context.Context) error {
	_, err := s.complete(ctx, chatCompletionRequest{
		Model: s.Model,
		Messages: []chatMessage{{
			Role:    "user",
			Content: `Return exactly {"status":"ok"}`,
		}},
	})
	return err
}

func (s *Service) complete(ctx context.Context, reqBody chatCompletionRequest) (*chatCompletionResponse, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", s.Authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tg-channel-to-rss")

	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, fmt.Errorf("openrouter request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(payload)))
	}

	var parsed chatCompletionResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Service) runTool(ctx context.Context, call toolCall) (string, error) {
	if call.Function.Name != "get_polymarket_events" {
		return "", fmt.Errorf("unknown tool %q", call.Function.Name)
	}
	if s.Polymarket == nil {
		return "", fmt.Errorf("polymarket tool is not configured")
	}
	var args struct {
		Tag string `json:"tag"`
	}
	if strings.TrimSpace(call.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("decode tool arguments: %w", err)
		}
	}
	return s.Polymarket.EventsByTag(ctx, args.Tag)
}

func parseAnalysis(content string) (AnalysisResult, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return AnalysisResult{}, fmt.Errorf("openrouter returned empty content")
	}

	var out AnalysisResult
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return AnalysisResult{}, fmt.Errorf("openrouter returned non-JSON content: %s", trimmed)
	}
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err != nil {
		return AnalysisResult{}, err
	}
	return out, nil
}

func fallbackTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Новость без заголовка"
	}
	const max = 80
	if len(text) <= max {
		return text
	}
	return text[:max]
}
