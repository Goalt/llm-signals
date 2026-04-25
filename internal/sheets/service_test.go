package sheets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppendRowBuildsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"updates":{}}`))
	}))
	defer server.Close()

	svc := NewService("Bearer sheet", "doc123", "Sheet1", server.Client())
	svc.BaseURL = server.URL
	err := svc.AppendRow(context.Background(), Row{ID: "1", Text: "hello", Type: "telegram", Source: "https://t.me", Metadata: `{}`, Action: "-", CreatedAt: "1", UpdatedAt: "1"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !strings.Contains(gotPath, "/doc123/values/Sheet1%21A:H:append?") {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotAuth != "Bearer sheet" {
		t.Fatalf("unexpected auth: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"values":[["1","hello","telegram","https://t.me","{}","-","1","1"]]`) {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}

func TestUpdateActionFindsAndUpdatesRow(t *testing.T) {
	var gotUpdatePath string
	var gotUpdateBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/values/Sheet1!A:H") || strings.Contains(r.URL.RequestURI(), "/values/Sheet1%21A:H"):
			_, _ = w.Write([]byte(`{"values":[["header"],["id-1","text"],["id-2","text"]]}`))
		case strings.Contains(r.URL.Path, "/values/Sheet1!F3") || strings.Contains(r.URL.RequestURI(), "/values/Sheet1%21F3"):
			gotUpdatePath = r.URL.RequestURI()
			body, _ := io.ReadAll(r.Body)
			gotUpdateBody = string(body)
			_, _ = w.Write([]byte(`{"updatedCells":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService("Bearer sheet", "doc123", "Sheet1", server.Client())
	svc.BaseURL = server.URL
	if err := svc.UpdateAction(context.Background(), "id-2", "Игнорировать новость"); err != nil {
		t.Fatalf("update action: %v", err)
	}
	if !strings.Contains(gotUpdatePath, "/doc123/values/Sheet1%21F3?") {
		t.Fatalf("unexpected update path: %q", gotUpdatePath)
	}
	if !strings.Contains(gotUpdateBody, `"Игнорировать новость"`) {
		t.Fatalf("unexpected update body: %s", gotUpdateBody)
	}
}

func TestSheetsErrors(t *testing.T) {
	t.Run("row not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"values":[["id"]]}`))
		}))
		defer server.Close()
		svc := NewService("Bearer sheet", "doc123", "Sheet1", server.Client())
		svc.BaseURL = server.URL
		if err := svc.UpdateAction(context.Background(), "missing", "x"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		svc := NewService("", "", "", http.DefaultClient)
		if err := svc.CheckAccess(context.Background()); err == nil {
			t.Fatalf("expected validation error")
		}
	})
}
