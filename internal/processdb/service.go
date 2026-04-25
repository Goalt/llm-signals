package processdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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

type Service struct {
	Path  string
	Table string

	db          *sql.DB
	migrateOnce sync.Once
	migrateErr  error
}

func NewService(path, table string) *Service {
	return &Service{Path: path, Table: table}
}

func (s *Service) AppendRow(ctx context.Context, row Row) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, text, type, source, metadata, action, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
text = excluded.text,
type = excluded.type,
source = excluded.source,
metadata = excluded.metadata,
action = excluded.action,
created_at = excluded.created_at,
updated_at = excluded.updated_at
`, s.Table), row.ID, row.Text, row.Type, row.Source, row.Metadata, row.Action, row.CreatedAt, row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("append sqlite row: %w", err)
	}
	return nil
}

func (s *Service) UpdateAction(ctx context.Context, id, action string) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET action = ?, updated_at = ? WHERE id = ?`, s.Table), action, fmt.Sprintf("%d", time.Now().UnixMilli()), id)
	if err != nil {
		return fmt.Errorf("update sqlite action: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read sqlite affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("sqlite row %q not found", id)
	}
	return nil
}

func (s *Service) CheckAccess(ctx context.Context) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("check sqlite access: %w", err)
	}
	if one != 1 {
		return fmt.Errorf("unexpected sqlite readiness result: %d", one)
	}
	return nil
}

func (s *Service) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Service) ensureReady() error {
	if s.Path == "" {
		return fmt.Errorf("sqlite path is required")
	}
	if s.Table == "" {
		return fmt.Errorf("sqlite table is required")
	}
	if !identifierPattern.MatchString(s.Table) {
		return fmt.Errorf("invalid sqlite table name %q", s.Table)
	}
	if s.db == nil {
		if err := ensureParentDir(s.Path); err != nil {
			return err
		}
		db, err := sql.Open("sqlite", s.Path)
		if err != nil {
			return fmt.Errorf("open sqlite db: %w", err)
		}
		s.db = db
	}
	s.migrateOnce.Do(func() {
		_, s.migrateErr = s.db.Exec(fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
id TEXT PRIMARY KEY,
text TEXT NOT NULL,
type TEXT NOT NULL,
source TEXT NOT NULL,
metadata TEXT NOT NULL,
action TEXT NOT NULL,
created_at TEXT NOT NULL,
updated_at TEXT NOT NULL
)
`, s.Table))
		if s.migrateErr != nil {
			s.migrateErr = fmt.Errorf("migrate sqlite schema: %w", s.migrateErr)
		}
	})
	return s.migrateErr
}

func ensureParentDir(path string) error {
	if path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sqlite directory: %w", err)
	}
	return nil
}
