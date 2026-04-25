package processanalyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/openrouter"
	"github.com/Goalt/tg-channel-to-rss/internal/processdb"
)

type RecordStore interface {
	AppendRow(ctx context.Context, row processdb.Row) error
	UpdateAction(ctx context.Context, id, action string) error
}

type Analyzer interface {
	AnalyzeNews(ctx context.Context, input openrouter.AnalyzeInput) (openrouter.AnalysisResult, error)
}

type TelegramSender interface {
	Send(ctx context.Context, text string) error
}

type Processor struct {
	Store    RecordStore
	Analyzer Analyzer
	Telegram TelegramSender
	Now      func() time.Time
}

type Result struct {
	Status string                     `json:"status"`
	Output *openrouter.AnalysisResult `json:"output,omitempty"`
	Reason string                     `json:"reason,omitempty"`
}

func NewProcessor(store RecordStore, analyzer Analyzer, telegram TelegramSender) *Processor {
	return &Processor{
		Store:    store,
		Analyzer: analyzer,
		Telegram: telegram,
		Now:      time.Now,
	}
}

func (p *Processor) Process(ctx context.Context, payload notifier.Payload) (Result, error) {
	if p.Store == nil || p.Analyzer == nil || p.Telegram == nil {
		return Result{}, fmt.Errorf("process-analyze processor is not fully configured")
	}
	now := p.Now()
	if err := p.Store.AppendRow(ctx, processdb.Row{
		ID:        payload.ID,
		Text:      payload.Item.Content,
		Type:      payload.SourceType,
		Source:    payload.SourceURL,
		Metadata:  stringifyMetadata(payload.Item.Metadata),
		Action:    "-",
		CreatedAt: strconv.FormatInt(now.UnixMilli(), 10),
		UpdatedAt: strconv.FormatInt(now.UnixMilli(), 10),
	}); err != nil {
		return Result{}, err
	}

	cleanText := strings.TrimSpace(removeBracketed(payload.Item.Content))
	if cleanText == "" {
		return Result{Status: "skipped", Reason: "empty text after cleanup"}, nil
	}

	analysis, err := p.Analyzer.AnalyzeNews(ctx, openrouter.AnalyzeInput{
		ID:       payload.ID,
		Source:   payload.SourceURL,
		Text:     cleanText,
		Metadata: payload.Item.Metadata,
	})
	if err != nil {
		return Result{}, err
	}
	if err := p.Telegram.Send(ctx, formatTelegramMessage(analysis)); err != nil {
		return Result{}, err
	}
	if err := p.Store.UpdateAction(ctx, analysis.ID, analysis.Action); err != nil {
		return Result{}, err
	}
	return Result{Status: "processed", Output: &analysis}, nil
}

func removeBracketed(text string) string {
	var out strings.Builder
	depth := 0
	for _, r := range text {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			} else {
				out.WriteRune(r)
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func stringifyMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return "{}"
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return "{}"
	}
	return string(body)
}

func formatTelegramMessage(result openrouter.AnalysisResult) string {
	return fmt.Sprintf("Новость: %s\n\nДействие: %s\n\nОбъяснение: %s\n\nТэги: %s",
		result.Title,
		result.Action,
		result.Explanation,
		result.Tags,
	)
}
