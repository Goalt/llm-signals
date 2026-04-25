package processanalyze

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/notifier"
	"github.com/Goalt/tg-channel-to-rss/internal/openrouter"
	"github.com/Goalt/tg-channel-to-rss/internal/processdb"
)

type fakeStore struct {
	appendRows []processdb.Row
	updatedID  string
	updatedAct string
	appendErr  error
	updateErr  error
}

func (f *fakeStore) AppendRow(_ context.Context, row processdb.Row) error {
	f.appendRows = append(f.appendRows, row)
	return f.appendErr
}

func (f *fakeStore) UpdateAction(_ context.Context, id, action string) error {
	f.updatedID = id
	f.updatedAct = action
	return f.updateErr
}

type fakeAnalyzer struct {
	input AnalyzeInputCapture
	out   openrouter.AnalysisResult
	err   error
}

type AnalyzeInputCapture struct {
	ID       string
	Source   string
	Text     string
	Metadata map[string]any
}

func (f *fakeAnalyzer) AnalyzeNews(_ context.Context, input openrouter.AnalyzeInput) (openrouter.AnalysisResult, error) {
	f.input = AnalyzeInputCapture{ID: input.ID, Source: input.Source, Text: input.Text, Metadata: input.Metadata}
	return f.out, f.err
}

type fakeTelegram struct {
	messages []string
	err      error
}

func (f *fakeTelegram) Send(_ context.Context, text string) error {
	f.messages = append(f.messages, text)
	return f.err
}

func TestProcessHappyPath(t *testing.T) {
	store := &fakeStore{}
	analyzer := &fakeAnalyzer{out: openrouter.AnalysisResult{Title: "Новость", ID: "delivery-1", Action: "Купить", Explanation: "Потому что.", Tags: "#рынок"}}
	telegram := &fakeTelegram{}
	processor := NewProcessor(store, analyzer, telegram)
	processor.Now = func() time.Time { return time.UnixMilli(1234) }

	result, err := processor.Process(context.Background(), notifier.Payload{
		ID:         "delivery-1",
		SourceType: notifier.SourceTypeTelegram,
		SourceURL:  "https://t.me/s/source",
		Item: app.FeedItemJSON{
			Content:  "Hello [https://link] world",
			Metadata: map[string]any{"foo": "bar"},
		},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Status != "processed" || result.Output == nil {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(store.appendRows) != 1 || store.appendRows[0].CreatedAt != "1234" {
		t.Fatalf("unexpected appended rows: %+v", store.appendRows)
	}
	if analyzer.input.Text != "Hello  world" {
		t.Fatalf("expected cleaned text, got %q", analyzer.input.Text)
	}
	if store.updatedID != "delivery-1" || store.updatedAct != "Купить" {
		t.Fatalf("unexpected sqlite update: id=%q action=%q", store.updatedID, store.updatedAct)
	}
	if len(telegram.messages) != 1 || !strings.Contains(telegram.messages[0], "Действие: Купить") {
		t.Fatalf("unexpected telegram messages: %+v", telegram.messages)
	}
}

func TestProcessSkippedWhenTextBecomesEmpty(t *testing.T) {
	store := &fakeStore{}
	analyzer := &fakeAnalyzer{}
	telegram := &fakeTelegram{}
	processor := NewProcessor(store, analyzer, telegram)

	result, err := processor.Process(context.Background(), notifier.Payload{
		ID:   "delivery-2",
		Item: app.FeedItemJSON{Content: "[link]"},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Status != "skipped" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if analyzer.input.Text != "" || len(telegram.messages) != 0 || store.updatedID != "" {
		t.Fatalf("unexpected side effects: analyzer=%+v telegram=%+v update=%q", analyzer.input, telegram.messages, store.updatedID)
	}
}

func TestProcessPropagatesErrors(t *testing.T) {
	t.Run("append error", func(t *testing.T) {
		processor := NewProcessor(&fakeStore{appendErr: errors.New("append failed")}, &fakeAnalyzer{}, &fakeTelegram{})
		if _, err := processor.Process(context.Background(), notifier.Payload{}); err == nil || !strings.Contains(err.Error(), "append failed") {
			t.Fatalf("expected append error, got %v", err)
		}
	})

	t.Run("analyzer error", func(t *testing.T) {
		store := &fakeStore{}
		processor := NewProcessor(store, &fakeAnalyzer{err: errors.New("llm failed")}, &fakeTelegram{})
		_, err := processor.Process(context.Background(), notifier.Payload{ID: "1", Item: app.FeedItemJSON{Content: "hello"}})
		if err == nil || !strings.Contains(err.Error(), "llm failed") {
			t.Fatalf("expected analyzer error, got %v", err)
		}
		if store.updatedID != "" {
			t.Fatalf("did not expect sqlite update on failure")
		}
	})
}
