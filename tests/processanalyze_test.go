package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/openrouter"
	"github.com/Goalt/tg-channel-to-rss/internal/processanalyze"
	"github.com/Goalt/tg-channel-to-rss/internal/processdb"
)

type blackBoxStore struct {
	rows    []processdb.Row
	updated map[string]string
}

func (b *blackBoxStore) AppendRow(_ context.Context, row processdb.Row) error {
	b.rows = append(b.rows, row)
	return nil
}

func (b *blackBoxStore) UpdateAction(_ context.Context, id, action string) error {
	if b.updated == nil {
		b.updated = map[string]string{}
	}
	b.updated[id] = action
	return nil
}

type blackBoxAnalyzer struct{}

func (blackBoxAnalyzer) AnalyzeNews(_ context.Context, input openrouter.AnalyzeInput) (openrouter.AnalysisResult, error) {
	return openrouter.AnalysisResult{
		Title:       input.Text,
		ID:          input.ID,
		Source:      input.Source,
		Action:      "Игнорировать новость",
		Explanation: "Слишком мало сигнала.",
		Tags:        "#рынок",
	}, nil
}

type blackBoxTelegram struct{ messages []string }

func (b *blackBoxTelegram) Send(_ context.Context, text string) error {
	b.messages = append(b.messages, text)
	return nil
}

func TestProcessAnalyze_BlackBoxFlow(t *testing.T) {
	store := &blackBoxStore{}
	telegram := &blackBoxTelegram{}
	processor := processanalyze.NewProcessor(store, blackBoxAnalyzer{}, telegram)
	processor.Now = func() time.Time { return time.UnixMilli(1000) }

	result, err := processor.Process(context.Background(), notifier.Payload{
		ID:         "delivery-9",
		SourceType: notifier.SourceTypeTelegram,
		SourceURL:  "https://t.me/s/durov",
		Item: app.FeedItemJSON{
			Content:  "Signal [ignore me] market moving",
			Metadata: map[string]any{"views": "10"},
		},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Output == nil || result.Output.Action != "Игнорировать новость" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(store.rows) != 1 {
		t.Fatalf("expected 1 appended row, got %d", len(store.rows))
	}
	if got := store.updated["delivery-9"]; got != "Игнорировать новость" {
		t.Fatalf("unexpected updated action: %q", got)
	}
	if len(telegram.messages) != 1 || !strings.Contains(telegram.messages[0], "Тэги: #рынок") {
		t.Fatalf("unexpected telegram messages: %+v", telegram.messages)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(store.rows[0].Metadata), &metadata); err != nil {
		t.Fatalf("metadata should be valid json: %v", err)
	}
	if metadata["views"] != "10" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}
