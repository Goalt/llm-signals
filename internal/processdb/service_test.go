package processdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteStoreAppendAndUpdate(t *testing.T) {
	svc := NewService(filepath.Join(t.TempDir(), "process-analyze.db"), "process_analyze_news")
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.AppendRow(context.Background(), Row{
		ID:        "delivery-1",
		Text:      "hello",
		Type:      "telegram",
		Source:    "https://t.me/s/test",
		Metadata:  `{"views":"10"}`,
		Action:    "-",
		CreatedAt: "1000",
		UpdatedAt: "1000",
	}); err != nil {
		t.Fatalf("AppendRow: %v", err)
	}
	if err := svc.UpdateAction(context.Background(), "delivery-1", "Купить"); err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}

	var row Row
	if err := svc.db.QueryRow(`SELECT id, text, type, source, metadata, action, created_at, updated_at FROM process_analyze_news WHERE id = ?`, "delivery-1").Scan(
		&row.ID,
		&row.Text,
		&row.Type,
		&row.Source,
		&row.Metadata,
		&row.Action,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if row.Action != "Купить" || row.Text != "hello" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestSQLiteStoreCheckAccessAndValidation(t *testing.T) {
	t.Run("check access", func(t *testing.T) {
		svc := NewService(filepath.Join(t.TempDir(), "process-analyze.db"), "process_analyze_news")
		t.Cleanup(func() { _ = svc.Close() })
		if err := svc.CheckAccess(context.Background()); err != nil {
			t.Fatalf("CheckAccess: %v", err)
		}
	})

	t.Run("missing row", func(t *testing.T) {
		svc := NewService(filepath.Join(t.TempDir(), "process-analyze.db"), "process_analyze_news")
		t.Cleanup(func() { _ = svc.Close() })
		if err := svc.UpdateAction(context.Background(), "missing", "x"); err == nil {
			t.Fatalf("expected not-found error")
		}
	})

	t.Run("invalid table", func(t *testing.T) {
		svc := NewService(filepath.Join(t.TempDir(), "process-analyze.db"), "bad-name")
		t.Cleanup(func() { _ = svc.Close() })
		if err := svc.CheckAccess(context.Background()); err == nil {
			t.Fatalf("expected validation error")
		}
	})

	t.Run("memory db", func(t *testing.T) {
		svc := NewService(":memory:", "process_analyze_news")
		t.Cleanup(func() { _ = svc.Close() })
		if err := svc.AppendRow(context.Background(), Row{ID: "1", Text: "t", Type: "telegram", Source: "s", Metadata: `{}`, Action: "-", CreatedAt: "1", UpdatedAt: "1"}); err != nil {
			t.Fatalf("AppendRow memory: %v", err)
		}
	})
}

func TestSQLiteStoreCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "process-analyze.db")
	svc := NewService(path, "process_analyze_news")
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.CheckAccess(context.Background()); err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	if _, err := sql.Open("sqlite", path); err != nil {
		t.Fatalf("re-open sqlite db: %v", err)
	}
}
