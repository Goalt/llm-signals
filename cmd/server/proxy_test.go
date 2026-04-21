package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatchesProxyRoute(t *testing.T) {
	if !matchesProxyRoute("/proxy/hyperliquid", "/proxy/hyperliquid") {
		t.Fatalf("expected exact route to match")
	}
	if !matchesProxyRoute("/proxy/hyperliquid/info", "/proxy/hyperliquid") {
		t.Fatalf("expected nested route to match")
	}
	if matchesProxyRoute("/proxy/hyperliquidx", "/proxy/hyperliquid") {
		t.Fatalf("unexpected prefix-only match")
	}
}

func TestPathSuffixNonMatchingRoute(t *testing.T) {
	if got := pathSuffix("/other", "/proxy/hyperliquid"); got != "/" {
		t.Fatalf("expected fallback suffix, got %q", got)
	}
}

func TestJoinURLPath(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{name: "both normalized", base: "/api", suffix: "/v1", want: "/api/v1"},
		{name: "base with trailing slash", base: "/api/", suffix: "/v1", want: "/api/v1"},
		{name: "suffix without leading slash", base: "/api", suffix: "v1", want: "/api/v1"},
		{name: "root base and root suffix", base: "/", suffix: "/", want: "/"},
		{name: "empty base and suffix", base: "", suffix: "", want: "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinURLPath(tc.base, tc.suffix); got != tc.want {
				t.Fatalf("joinURLPath(%q, %q)=%q want %q", tc.base, tc.suffix, got, tc.want)
			}
		})
	}
}

func TestNewAPIProxyForwardsAndInjectsAuthorization(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotAuth string
	var gotBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	proxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/hyperliquid",
		TargetBaseURL: upstream.URL + "/api",
		Authorization: "Bearer server-token",
		Name:          "hyperliquid",
	})
	if err != nil {
		t.Fatalf("unexpected proxy error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/hyperliquid/v1/orders?foo=bar", strings.NewReader(`{"a":1}`))
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if gotPath != "/api/v1/orders" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotQuery != "foo=bar" {
		t.Fatalf("unexpected query: %q", gotQuery)
	}
	if gotAuth != "Bearer server-token" {
		t.Fatalf("unexpected auth: %q", gotAuth)
	}
	if gotBody != `{"a":1}` {
		t.Fatalf("unexpected body: %q", gotBody)
	}
}

func TestNewAPIProxyWithoutAuthorization(t *testing.T) {
	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, err := newAPIProxy(apiProxyConfig{
		RoutePrefix:   "/proxy/polymarket",
		TargetBaseURL: upstream.URL,
		Name:          "polymarket",
	})
	if err != nil {
		t.Fatalf("unexpected proxy error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxy/polymarket", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if gotAuth != "" {
		t.Fatalf("expected empty auth, got %q", gotAuth)
	}
}
