package main

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Goalt/tg-channel-to-rss/internal/processanalyze"
)

const datastarCDNURL = "https://cdn.jsdelivr.net/gh/starfederation/datastar@v1.0.1/bundles/datastar.js"

var dashboardSQLiteIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type dashboardRuntime struct {
	mu sync.Mutex

	startedAt time.Time
	now       func() time.Time
	getenv    func(string) string

	proxyConfigs []apiProxyConfig
	total        int
	errors       int
	groupCounts  map[string]int
	recent       []dashboardRequest
	maxRecent    int
}

type dashboardRequest struct {
	At       time.Time
	Method   string
	Path     string
	Group    string
	Status   int
	Duration time.Duration
}

type dashboardSnapshot struct {
	StartedAt   time.Time
	Now         time.Time
	Total       int
	Errors      int
	GroupCounts map[string]int
	Recent      []dashboardRequest
}

type dashboardSourceStatus struct {
	Name   string
	Type   string
	State  string
	Detail string
	Action string
}

type dashboardProxyStatus struct {
	Name        string
	RoutePrefix string
	BaseURL     string
	AuthState   string
	Requests    int
}

type dashboardConfigStatus struct {
	Name  string
	State string
	Scope string
	Note  string
}

type dashboardGoMetrics struct {
	Version    string
	Compiler   string
	GOMAXPROCS int
	Goroutines int
	HeapAlloc  string
	HeapSys    string
	StackInUse string
	NumGC      uint32
	LastGC     string
}

type dashboardSQLiteMetrics struct {
	State         string
	Path          string
	Table         string
	FileSize      string
	LogicalSize   string
	RowCount      string
	LastModified  string
	SQLiteVersion string
	Detail        string
}

type dashboardSourcesView struct {
	Type    string
	State   string
	Page    int
	PerPage int
}

type dashboardSourcesPage struct {
	Items      []dashboardSourceStatus
	Total      int
	Page       int
	PerPage    int
	TotalPages int
	Start      int
	End        int
	Type       string
	State      string
}

func newDashboardRuntime(getenv func(string) string, proxyConfigs []apiProxyConfig) *dashboardRuntime {
	copied := make([]apiProxyConfig, len(proxyConfigs))
	copy(copied, proxyConfigs)

	return &dashboardRuntime{
		startedAt:    time.Now(),
		now:          time.Now,
		getenv:       getenv,
		proxyConfigs: copied,
		groupCounts:  map[string]int{},
		maxRecent:    40,
	}
}

func (rt *dashboardRuntime) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := rt.now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		rt.recordRequest(dashboardRequest{
			At:       started,
			Method:   r.Method,
			Path:     r.URL.Path,
			Group:    classifyDashboardGroup(r.URL.Path),
			Status:   rec.status,
			Duration: rt.now().Sub(started),
		})
	})
}

func (rt *dashboardRuntime) recordRequest(req dashboardRequest) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.total++
	if req.Status >= 400 {
		rt.errors++
	}
	rt.groupCounts[req.Group]++
	rt.recent = append([]dashboardRequest{req}, rt.recent...)
	if len(rt.recent) > rt.maxRecent {
		rt.recent = rt.recent[:rt.maxRecent]
	}
}

func (rt *dashboardRuntime) snapshot() dashboardSnapshot {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	groupCounts := make(map[string]int, len(rt.groupCounts))
	for k, v := range rt.groupCounts {
		groupCounts[k] = v
	}
	recent := make([]dashboardRequest, len(rt.recent))
	copy(recent, rt.recent)

	return dashboardSnapshot{
		StartedAt:   rt.startedAt,
		Now:         rt.now(),
		Total:       rt.total,
		Errors:      rt.errors,
		GroupCounts: groupCounts,
		Recent:      recent,
	}
}

func classifyDashboardGroup(path string) string {
	switch {
	case path == "/dashboard" || path == "/dashboard/" || strings.HasPrefix(path, "/dashboard/"):
		return "dashboard"
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		return "mcp"
	case path == "/process-analyze" || path == "/process-analyze/":
		return "process-analyze"
	case matchesProxyRoute(path, "/proxy/hyperliquid"):
		return "proxy:hyperliquid"
	case matchesProxyRoute(path, "/proxy/polymarket"):
		return "proxy:polymarket"
	case matchesProxyRoute(path, "/proxy/bybit"):
		return "proxy:bybit"
	case strings.HasPrefix(path, "/feed/"):
		return "feed"
	default:
		return "other"
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func newDashboardHandler(rt *dashboardRuntime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		switch r.URL.Path {
		case "/dashboard", "/dashboard/":
			writeDashboardPage(w, rt)
		case "/dashboard/partials/all":
			writeDashboardPartials(w, rt)
		case "/dashboard/partials/overview":
			writeHTML(w, renderOverviewSection(rt))
		case "/dashboard/partials/runtime":
			writeHTML(w, renderRuntimeSection(rt))
		case "/dashboard/partials/news":
			writeHTML(w, renderNewsSection(rt, parseDashboardNewsView(r.URL.Query())))
		case "/dashboard/partials/sources":
			writeHTML(w, renderSourcesSection(rt, parseDashboardSourcesView(r.URL.Query())))
		case "/dashboard/partials/requests":
			writeHTML(w, renderRequestsSection(rt))
		case "/dashboard/partials/proxies":
			writeHTML(w, renderProxiesSection(rt))
		case "/dashboard/partials/config":
			writeHTML(w, renderConfigSection(rt))
		default:
			http.Error(w, "Not Found", http.StatusNotFound)
		}
	})
}

func writeDashboardPage(w http.ResponseWriter, rt *dashboardRuntime) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>llm-signals dashboard</title>
  <script type="module" src="%s"></script>
  <style>
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    body { margin: 0; font-family: Inter, system-ui, sans-serif; background: #0b1020; color: #e5eefc; }
    a { color: #93c5fd; }
    .page { max-width: 1240px; margin: 0 auto; padding: 24px; }
    .hero, .panel, .table-wrap { background: #111831; border: 1px solid #24304f; border-radius: 16px; }
    .hero { padding: 24px; margin-bottom: 16px; }
    .hero h1 { margin: 0 0 8px; font-size: 32px; }
    .hero p { margin: 0; color: #a8b3cf; }
    .hero-actions { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 16px; }
    button { border: 1px solid #35518b; background: #16213f; color: #eff6ff; border-radius: 10px; padding: 10px 14px; cursor: pointer; }
    button:hover { background: #1d2b52; }
    .panel { padding: 18px; margin-bottom: 16px; }
    .panel-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
    .panel-header h2, .panel-header h3 { margin: 0; }
    .panel-header h2 { font-size: 20px; }
    .panel-header h3 { font-size: 16px; }
    .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
    .card { background: #0d1530; border: 1px solid #1e2c4f; border-radius: 12px; padding: 14px; }
    .split { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; }
    .filters { display: grid; gap: 10px; margin-top: 12px; }
    .filter-group { display: grid; gap: 8px; }
    .chips { display: flex; flex-wrap: wrap; gap: 8px; }
    .chip, .pager-button { border: 1px solid #35518b; background: #16213f; color: #eff6ff; border-radius: 999px; padding: 8px 12px; cursor: pointer; }
    .chip:hover, .pager-button:hover { background: #1d2b52; }
    .chip-active { background: #2b4a8d; border-color: #7aa2ff; }
    .pager { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 12px; flex-wrap: wrap; }
    .pager-actions { display: flex; gap: 8px; flex-wrap: wrap; }
    .pager-button[disabled] { opacity: 0.45; cursor: not-allowed; }
    .label { display: block; font-size: 12px; color: #8ea2c8; text-transform: uppercase; letter-spacing: .04em; margin-bottom: 6px; }
    .value { display: block; font-size: 24px; font-weight: 700; }
    .subtle { color: #9fb0cf; font-size: 14px; }
    .state { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 700; border-radius: 999px; padding: 5px 10px; }
    .state-healthy { background: #13361f; color: #8df0a6; }
    .state-warning { background: #3a2c12; color: #f7d37a; }
    .state-disabled { background: #312033; color: #d8b4fe; }
    .state-degraded { background: #421c24; color: #f9a8b8; }
    .state-unknown { background: #24304f; color: #cbd5e1; }
    table { width: 100%%; border-collapse: collapse; }
    th, td { text-align: left; padding: 10px 12px; border-bottom: 1px solid #223055; vertical-align: top; overflow-wrap: anywhere; word-break: break-word; }
    th { font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: #8ea2c8; }
    .table-wrap { overflow: auto; }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
    .footer-note { margin-top: 12px; color: #8ea2c8; font-size: 13px; }
  </style>
</head>
<body>
  <div class="page" data-signals="{ section: 'overview' }">
    <section class="hero">
      <h1>llm-signals dashboard</h1>
      <p>Backend-driven monitoring and control surface for feeds, proxies, MCP, notifiers, Process + Analyze, and runtime metrics.</p>
      <div class="hero-actions">
        <button data-on:click="@get('/dashboard/partials/all')">Refresh all</button>
        <button data-on:click="$section = 'overview'">Overview</button>
        <button data-on:click="$section = 'news'">News</button>
        <button data-on:click="$section = 'sources'">Sources</button>
        <button data-on:click="$section = 'requests'">Requests</button>
        <button data-on:click="$section = 'proxies'">Proxies</button>
        <button data-on:click="$section = 'config'">Config</button>
      </div>
    </section>
    %s
    %s
    %s
    %s
    %s
    %s
    %s
  </div>
</body>
</html>`, datastarCDNURL, renderOverviewSection(rt), renderRuntimeSection(rt), renderNewsSection(rt, defaultDashboardNewsView()), renderSourcesSection(rt, defaultDashboardSourcesView()), renderRequestsSection(rt), renderProxiesSection(rt), renderConfigSection(rt))
}

func writeDashboardPartials(w http.ResponseWriter, rt *dashboardRuntime) {
	writeHTML(w,
		renderOverviewSection(rt)+
			renderRuntimeSection(rt)+
			renderNewsSection(rt, defaultDashboardNewsView())+
			renderSourcesSection(rt, defaultDashboardSourcesView())+
			renderRequestsSection(rt)+
			renderProxiesSection(rt)+
			renderConfigSection(rt),
	)
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	_, _ = w.Write([]byte(body))
}

func renderOverviewSection(rt *dashboardRuntime) string {
	snap := rt.snapshot()
	uptime := snap.Now.Sub(snap.StartedAt).Round(time.Second)
	errorRate := 0.0
	if snap.Total > 0 {
		errorRate = float64(snap.Errors) / float64(snap.Total) * 100
	}
	activeSources := 0
	for _, source := range rt.sourceStatuses() {
		if source.State == "healthy" || source.State == "warning" {
			activeSources++
		}
	}

	return fmt.Sprintf(`<section id="dashboard-overview" class="panel">
  <div class="panel-header">
    <h2>Operations overview</h2>
    <button data-on:click="@get('/dashboard/partials/overview')">Refresh</button>
  </div>
  <div class="cards">
    <div class="card"><span class="label">Uptime</span><span class="value">%s</span><span class="subtle">started %s</span></div>
    <div class="card"><span class="label">Total requests</span><span class="value">%d</span><span class="subtle">errors: %d (%.1f%%)</span></div>
    <div class="card"><span class="label">Feed traffic</span><span class="value">%d</span><span class="subtle">/feed/* requests since boot</span></div>
    <div class="card"><span class="label">Proxy traffic</span><span class="value">%d</span><span class="subtle">all /proxy/* routes</span></div>
    <div class="card"><span class="label">MCP traffic</span><span class="value">%d</span><span class="subtle">HTTP JSON-RPC calls</span></div>
    <div class="card"><span class="label">Active sources</span><span class="value">%d</span><span class="subtle">configured notifier/process surfaces</span></div>
  </div>
  <div class="footer-note">Dashboard is backend-rendered with Datastar partial refreshes; no separate frontend build is required.</div>
</section>`,
		html.EscapeString(uptime.String()),
		html.EscapeString(snap.StartedAt.Format(time.RFC3339)),
		snap.Total,
		snap.Errors,
		errorRate,
		snap.GroupCounts["feed"],
		rt.proxyRequestTotal(snap),
		snap.GroupCounts["mcp"],
		activeSources,
	)
}

func renderRuntimeSection(rt *dashboardRuntime) string {
	goMetrics := collectGoRuntimeMetrics()
	sqliteMetrics := rt.sqliteMetrics()

	return fmt.Sprintf(`<section id="dashboard-runtime" class="panel" data-show="$section == 'overview'">
  <div class="panel-header">
    <h2>Go and SQLite metrics</h2>
    <button data-on:click="@get('/dashboard/partials/runtime')">Refresh</button>
  </div>
  <div class="split">
    <div>
      <div class="panel-header"><h3>Go runtime</h3></div>
      <div class="cards">
        <div class="card"><span class="label">Go version</span><span class="value">%s</span><span class="subtle">compiler %s</span></div>
        <div class="card"><span class="label">Goroutines</span><span class="value">%d</span><span class="subtle">GOMAXPROCS %d</span></div>
        <div class="card"><span class="label">Heap alloc</span><span class="value">%s</span><span class="subtle">heap sys %s</span></div>
        <div class="card"><span class="label">GC cycles</span><span class="value">%d</span><span class="subtle">last GC %s</span></div>
      </div>
      <div class="table-wrap" style="margin-top: 12px;">
        <table>
          <thead><tr><th>Metric</th><th>Value</th></tr></thead>
          <tbody>
            <tr><td>Stack in use</td><td>%s</td></tr>
            <tr><td>Heap sys</td><td>%s</td></tr>
            <tr><td>Last GC</td><td>%s</td></tr>
          </tbody>
        </table>
      </div>
    </div>
    <div>
      <div class="panel-header"><h3>SQLite</h3></div>
      <div class="cards">
        <div class="card"><span class="label">SQLite state</span><span class="value">%s</span><span class="subtle">%s</span></div>
        <div class="card"><span class="label">SQLite file</span><span class="value">%s</span><span class="subtle">path %s</span></div>
        <div class="card"><span class="label">SQLite rows</span><span class="value">%s</span><span class="subtle">table %s</span></div>
        <div class="card"><span class="label">SQLite logical size</span><span class="value">%s</span><span class="subtle">version %s</span></div>
      </div>
      <div class="table-wrap" style="margin-top: 12px;">
        <table>
          <thead><tr><th>Metric</th><th>Value</th></tr></thead>
          <tbody>
            <tr><td>Path</td><td class="mono">%s</td></tr>
            <tr><td>Last modified</td><td>%s</td></tr>
            <tr><td>Detail</td><td>%s</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</section>`,
		html.EscapeString(goMetrics.Version),
		html.EscapeString(goMetrics.Compiler),
		goMetrics.Goroutines,
		goMetrics.GOMAXPROCS,
		html.EscapeString(goMetrics.HeapAlloc),
		html.EscapeString(goMetrics.HeapSys),
		goMetrics.NumGC,
		html.EscapeString(goMetrics.LastGC),
		html.EscapeString(goMetrics.StackInUse),
		html.EscapeString(goMetrics.HeapSys),
		html.EscapeString(goMetrics.LastGC),
		html.EscapeString(strings.ToUpper(sqliteMetrics.State)),
		html.EscapeString(sqliteMetrics.Detail),
		html.EscapeString(sqliteMetrics.FileSize),
		html.EscapeString(sqliteMetrics.Path),
		html.EscapeString(sqliteMetrics.RowCount),
		html.EscapeString(sqliteMetrics.Table),
		html.EscapeString(sqliteMetrics.LogicalSize),
		html.EscapeString(sqliteMetrics.SQLiteVersion),
		html.EscapeString(sqliteMetrics.Path),
		html.EscapeString(sqliteMetrics.LastModified),
		stateBadge(sqliteMetrics.State)+" "+html.EscapeString(sqliteMetrics.Detail),
	)
}

func defaultDashboardSourcesView() dashboardSourcesView {
	return dashboardSourcesView{Type: "all", State: "all", Page: 1, PerPage: 2}
}

func parseDashboardSourcesView(values url.Values) dashboardSourcesView {
	view := defaultDashboardSourcesView()
	view.Type = normalizeDashboardSourcesType(values.Get("type"))
	view.State = normalizeDashboardSourcesState(values.Get("state"))
	view.Page = parseDashboardSourcesInt(values.Get("page"), 1)
	view.PerPage = normalizeDashboardSourcesPerPage(values.Get("per_page"))
	return view
}

func normalizeDashboardSourcesView(view dashboardSourcesView) dashboardSourcesView {
	view.Type = normalizeDashboardSourcesType(view.Type)
	view.State = normalizeDashboardSourcesState(view.State)
	if view.Page < 1 {
		view.Page = 1
	}
	view.PerPage = normalizeDashboardSourcesPerPage(strconv.Itoa(view.PerPage))
	return view
}

func normalizeDashboardSourcesType(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "all":
		return "all"
	case "telegram", "x", "polymarket", "pipeline":
		return raw
	default:
		return "all"
	}
}

func normalizeDashboardSourcesState(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "all":
		return "all"
	case "healthy", "warning", "disabled", "degraded":
		return raw
	default:
		return "all"
	}
}

func normalizeDashboardSourcesPerPage(raw string) int {
	value := parseDashboardSourcesInt(raw, defaultDashboardSourcesView().PerPage)
	switch value {
	case 2, 3, 4, 8:
		return value
	default:
		return defaultDashboardSourcesView().PerPage
	}
}

func parseDashboardSourcesInt(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func (v dashboardSourcesView) withType(value string) dashboardSourcesView {
	v.Type = value
	v.Page = 1
	return v
}

func (v dashboardSourcesView) withState(value string) dashboardSourcesView {
	v.State = value
	v.Page = 1
	return v
}

func (v dashboardSourcesView) withPerPage(value int) dashboardSourcesView {
	v.PerPage = value
	v.Page = 1
	return v
}

func (v dashboardSourcesView) withPage(value int) dashboardSourcesView {
	v.Page = value
	return v
}

func dashboardSourcesURL(view dashboardSourcesView) string {
	base := "/dashboard/partials/sources"
	values := url.Values{}
	if view.Type != "" && view.Type != "all" {
		values.Set("type", view.Type)
	}
	if view.State != "" && view.State != "all" {
		values.Set("state", view.State)
	}
	if view.Page > 1 {
		values.Set("page", strconv.Itoa(view.Page))
	}
	if view.PerPage > 0 && view.PerPage != defaultDashboardSourcesView().PerPage {
		values.Set("per_page", strconv.Itoa(view.PerPage))
	}
	if query := values.Encode(); query != "" {
		return base + "?" + query
	}
	return base
}

func filterDashboardSources(sources []dashboardSourceStatus, view dashboardSourcesView) []dashboardSourceStatus {
	filtered := make([]dashboardSourceStatus, 0, len(sources))
	for _, source := range sources {
		if view.Type != "all" && source.Type != view.Type {
			continue
		}
		if view.State != "all" && source.State != view.State {
			continue
		}
		filtered = append(filtered, source)
	}
	return filtered
}

func paginateDashboardSources(sources []dashboardSourceStatus, view dashboardSourcesView) dashboardSourcesPage {
	total := len(sources)
	page := view.Page
	if page < 1 {
		page = 1
	}
	perPage := view.PerPage
	if perPage < 1 {
		perPage = defaultDashboardSourcesView().PerPage
	}
	totalPages := 1
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	if page > totalPages {
		page = totalPages
	}
	start := 0
	end := 0
	items := make([]dashboardSourceStatus, 0)
	if total > 0 {
		start = (page - 1) * perPage
		if start > total {
			start = total
		}
		end = start + perPage
		if end > total {
			end = total
		}
		items = append(items, sources[start:end]...)
	}
	from := 0
	if len(items) > 0 {
		from = start + 1
	}
	return dashboardSourcesPage{
		Items:      items,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
		Start:      from,
		End:        end,
		Type:       view.Type,
		State:      view.State,
	}
}

func renderSourcesSection(rt *dashboardRuntime, view dashboardSourcesView) string {
	view = normalizeDashboardSourcesView(view)
	sources := paginateDashboardSources(filterDashboardSources(rt.sourceStatuses(), view), view)

	var rows strings.Builder
	if len(sources.Items) == 0 {
		rows.WriteString(`<tr><td colspan="5" class="subtle">No sources match the current filters.</td></tr>`)
	} else {
		for _, source := range sources.Items {
			fmt.Fprintf(&rows, `<tr><td>%s</td><td class="mono">%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				html.EscapeString(source.Name),
				html.EscapeString(source.Type),
				stateBadge(source.State),
				html.EscapeString(source.Detail),
				html.EscapeString(source.Action),
			)
		}
	}

	var stateButtons strings.Builder
	for _, option := range []struct{ Value, Label string }{
		{Value: "all", Label: "All"},
		{Value: "healthy", Label: "Healthy"},
		{Value: "warning", Label: "Warning"},
		{Value: "disabled", Label: "Disabled"},
		{Value: "degraded", Label: "Degraded"},
	} {
		buttonView := view.withState(option.Value)
		fmt.Fprintf(&stateButtons, `<button type="button" class="%s" data-on:click="@get('%s')">%s</button>`,
			sourcesChipClass(view.State == option.Value),
			html.EscapeString(dashboardSourcesURL(buttonView)),
			html.EscapeString(option.Label),
		)
	}

	var typeButtons strings.Builder
	for _, option := range []struct{ Value, Label string }{
		{Value: "all", Label: "All"},
		{Value: "telegram", Label: "Telegram"},
		{Value: "x", Label: "x.com"},
		{Value: "polymarket", Label: "Polymarket"},
		{Value: "pipeline", Label: "Process + Analyze"},
	} {
		buttonView := view.withType(option.Value)
		fmt.Fprintf(&typeButtons, `<button type="button" class="%s" data-on:click="@get('%s')">%s</button>`,
			sourcesChipClass(view.Type == option.Value),
			html.EscapeString(dashboardSourcesURL(buttonView)),
			html.EscapeString(option.Label),
		)
	}

	var perPageButtons strings.Builder
	for _, option := range []int{2, 3, 4, 8} {
		buttonView := view.withPerPage(option)
		fmt.Fprintf(&perPageButtons, `<button type="button" class="%s" data-on:click="@get('%s')">%d</button>`,
			sourcesChipClass(view.PerPage == option),
			html.EscapeString(dashboardSourcesURL(buttonView)),
			option,
		)
	}

	showing := "No sources match the current filters."
	if sources.Total > 0 {
		showing = fmt.Sprintf("Showing %d-%d of %d source(s) · page %d of %d", sources.Start, sources.End, sources.Total, sources.Page, sources.TotalPages)
	}

	prevDisabled := sources.Page <= 1 || sources.Total == 0
	nextDisabled := sources.Page >= sources.TotalPages || sources.Total == 0
	prevURL := dashboardSourcesURL(view.withPage(sources.Page - 1))
	nextURL := dashboardSourcesURL(view.withPage(sources.Page + 1))

	return `<section id="dashboard-sources" class="panel" data-show="$section == 'sources' || $section == 'overview'">
  <div class="panel-header">
    <h2>Sources</h2>
    <button data-on:click="@get('/dashboard/partials/sources')">Refresh</button>
  </div>
  <div class="filters">
    <div class="filter-group">
      <span class="label">State</span>
      <div class="chips">` + stateButtons.String() + `</div>
    </div>
    <div class="filter-group">
      <span class="label">Type</span>
      <div class="chips">` + typeButtons.String() + `</div>
    </div>
    <div class="filter-group">
      <span class="label">Per page</span>
      <div class="chips">` + perPageButtons.String() + `</div>
    </div>
  </div>
  <div class="footer-note">` + html.EscapeString(showing) + `</div>
  <div class="table-wrap">
    <table>
      <thead><tr><th>Name</th><th>Type</th><th>State</th><th>Detail</th><th>Operator action</th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table>
  </div>
  <div class="pager">
    <div class="subtle">` + html.EscapeString(showing) + `</div>
    <div class="pager-actions">
      <button type="button" class="pager-button"` + disabledAttr(prevDisabled) + ` data-on:click="@get('` + html.EscapeString(prevURL) + `')">Prev</button>
      <button type="button" class="pager-button"` + disabledAttr(nextDisabled) + ` data-on:click="@get('` + html.EscapeString(nextURL) + `')">Next</button>
    </div>
  </div>
</section>`
}

func sourcesChipClass(active bool) string {
	if active {
		return "chip chip-active"
	}
	return "chip"
}

func disabledAttr(disabled bool) string {
	if disabled {
		return " disabled"
	}
	return ""
}

func renderRequestsSection(rt *dashboardRuntime) string {
	snap := rt.snapshot()
	var recent strings.Builder
	if len(snap.Recent) == 0 {
		recent.WriteString(`<tr><td colspan="6" class="subtle">No requests captured yet.</td></tr>`)
	} else {
		for _, req := range snap.Recent {
			fmt.Fprintf(&recent, `<tr><td class="mono">%s</td><td>%s</td><td class="mono">%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
				html.EscapeString(req.At.Format("15:04:05")),
				html.EscapeString(req.Method),
				html.EscapeString(req.Path),
				html.EscapeString(req.Group),
				req.Status,
				html.EscapeString(req.Duration.Round(time.Millisecond).String()),
			)
		}
	}

	type pair struct {
		Name  string
		Count int
	}
	pairs := make([]pair, 0, len(snap.GroupCounts))
	for name, count := range snap.GroupCounts {
		pairs = append(pairs, pair{Name: name, Count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count == pairs[j].Count {
			return pairs[i].Name < pairs[j].Name
		}
		return pairs[i].Count > pairs[j].Count
	})

	var cards strings.Builder
	if len(pairs) == 0 {
		cards.WriteString(`<div class="card"><span class="label">No traffic yet</span><span class="subtle">Request counters will appear after the first hit.</span></div>`)
	} else {
		for _, pair := range pairs {
			fmt.Fprintf(&cards, `<div class="card"><span class="label">%s</span><span class="value">%d</span><span class="subtle">requests since boot</span></div>`, html.EscapeString(pair.Name), pair.Count)
		}
	}

	return `<section id="dashboard-requests" class="panel" data-show="$section == 'requests' || $section == 'overview'">
  <div class="panel-header">
    <h2>Requests</h2>
    <button data-on:click="@get('/dashboard/partials/requests')">Refresh</button>
  </div>
  <div class="cards">` + cards.String() + `</div>
  <div class="table-wrap" style="margin-top: 12px;">
    <table>
      <thead><tr><th>Time</th><th>Method</th><th>Path</th><th>Group</th><th>Status</th><th>Duration</th></tr></thead>
      <tbody>` + recent.String() + `</tbody>
    </table>
  </div>
</section>`
}

func renderProxiesSection(rt *dashboardRuntime) string {
	snap := rt.snapshot()
	var rows strings.Builder
	for _, proxy := range rt.proxyStatuses(snap) {
		fmt.Fprintf(&rows, `<tr><td>%s</td><td class="mono">%s</td><td class="mono">%s</td><td>%s</td><td>%d</td></tr>`,
			html.EscapeString(proxy.Name),
			html.EscapeString(proxy.RoutePrefix),
			html.EscapeString(proxy.BaseURL),
			stateBadge(proxy.AuthState),
			proxy.Requests,
		)
	}

	return `<section id="dashboard-proxies" class="panel" data-show="$section == 'proxies' || $section == 'overview'">
  <div class="panel-header">
    <h2>Proxy monitor</h2>
    <button data-on:click="@get('/dashboard/partials/proxies')">Refresh</button>
  </div>
  <div class="table-wrap">
    <table>
      <thead><tr><th>Name</th><th>Route</th><th>Upstream</th><th>Auth</th><th>Requests</th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table>
  </div>
</section>`
}

func renderConfigSection(rt *dashboardRuntime) string {
	var rows strings.Builder
	for _, item := range rt.configStatuses() {
		fmt.Fprintf(&rows, `<tr><td class="mono">%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(item.Name),
			stateBadge(item.State),
			html.EscapeString(item.Scope),
			html.EscapeString(item.Note),
		)
	}

	return `<section id="dashboard-config" class="panel" data-show="$section == 'config' || $section == 'overview'">
  <div class="panel-header">
    <h2>Config status</h2>
    <button data-on:click="@get('/dashboard/partials/config')">Refresh</button>
  </div>
  <div class="table-wrap">
    <table>
      <thead><tr><th>Variable</th><th>State</th><th>Scope</th><th>Note</th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table>
  </div>
</section>`
}

func collectGoRuntimeMetrics() dashboardGoMetrics {
	version := runtime.Version()
	if info, ok := debug.ReadBuildInfo(); ok && info.GoVersion != "" {
		version = info.GoVersion
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	lastGC := "never"
	if mem.LastGC > 0 {
		lastGC = time.Unix(0, int64(mem.LastGC)).UTC().Format(time.RFC3339)
	}

	return dashboardGoMetrics{
		Version:    version,
		Compiler:   runtime.Compiler,
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		Goroutines: runtime.NumGoroutine(),
		HeapAlloc:  formatBytes(mem.HeapAlloc),
		HeapSys:    formatBytes(mem.HeapSys),
		StackInUse: formatBytes(mem.StackInuse),
		NumGC:      mem.NumGC,
		LastGC:     lastGC,
	}
}

func (rt *dashboardRuntime) sqliteMetrics() dashboardSQLiteMetrics {
	cfg, missing := processanalyze.LoadConfigFromEnv(rt.getenv)
	metrics := dashboardSQLiteMetrics{
		State:         "warning",
		Path:          cfg.SQLitePath,
		Table:         cfg.SQLiteTable,
		FileSize:      "n/a",
		LogicalSize:   "n/a",
		RowCount:      "n/a",
		LastModified:  "n/a",
		SQLiteVersion: "n/a",
	}
	if len(missing) > 0 {
		metrics.Detail = "process-analyze missing " + strings.Join(missing, ", ")
	}

	path := strings.TrimSpace(cfg.SQLitePath)
	if path == "" {
		metrics.State = "disabled"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "sqlite path is empty")
		return metrics
	}
	if path == ":memory:" {
		metrics.Detail = appendDashboardDetail(metrics.Detail, "in-memory sqlite has no persistent file metrics")
		return metrics
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			metrics.Detail = appendDashboardDetail(metrics.Detail, "sqlite file not created yet")
			return metrics
		}
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "stat sqlite file: "+err.Error())
		return metrics
	}

	metrics.FileSize = formatBytes(uint64(info.Size()))
	metrics.LastModified = info.ModTime().UTC().Format(time.RFC3339)

	if !dashboardSQLiteIdentifierPattern.MatchString(cfg.SQLiteTable) {
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, fmt.Sprintf("invalid sqlite table name %q", cfg.SQLiteTable))
		return metrics
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "open sqlite db: "+err.Error())
		return metrics
	}
	defer db.Close()
	if pingErr := db.Ping(); pingErr != nil {
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "ping sqlite db: "+pingErr.Error())
		return metrics
	}

	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&metrics.SQLiteVersion); err != nil {
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "read sqlite version: "+err.Error())
		return metrics
	}

	var pageCount, pageSize int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err == nil {
		if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err == nil && pageCount > 0 && pageSize > 0 {
			metrics.LogicalSize = formatBytes(uint64(pageCount * pageSize))
		}
	}

	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, cfg.SQLiteTable)
	var rowCount int64
	if err := db.QueryRow(query).Scan(&rowCount); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			metrics.Detail = appendDashboardDetail(metrics.Detail, "sqlite table not created yet")
			if len(missing) == 0 {
				metrics.State = "warning"
			}
			return metrics
		}
		metrics.State = "degraded"
		metrics.Detail = appendDashboardDetail(metrics.Detail, "read sqlite row count: "+err.Error())
		return metrics
	}
	metrics.RowCount = fmt.Sprintf("%d", rowCount)

	if len(missing) == 0 {
		metrics.State = "healthy"
		metrics.Detail = "sqlite accessible"
		return metrics
	}
	metrics.Detail = appendDashboardDetail(metrics.Detail, "sqlite accessible")
	return metrics
}

func appendDashboardDetail(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (rt *dashboardRuntime) sourceStatuses() []dashboardSourceStatus {
	getenv := rt.getenv
	telegramChannels := splitList(getenv("TG_CHANNELS"))
	webhooks := splitList(getenv("WEBHOOKS"))
	xUsers := splitList(getenv("X_USERS"))
	polymarketChannels := splitList(getenv("POLYMARKET_CHANNELS"))
	processCfg, missing := processanalyze.LoadConfigFromEnv(getenv)

	statuses := []dashboardSourceStatus{
		{
			Name:   "Telegram notifier",
			Type:   "telegram",
			State:  dashboardEnabledState(len(telegramChannels) > 0 && len(webhooks) > 0),
			Detail: fmt.Sprintf("%d channel(s), %d webhook(s), interval %s", len(telegramChannels), len(webhooks), dashboardEnvOrDefault(getenv, "POLL_INTERVAL", "5m")),
			Action: "Refresh or edit TG_CHANNELS / WEBHOOKS",
		},
		{
			Name:   "x.com notifier",
			Type:   "x",
			State:  dashboardEnabledState(len(xUsers) > 0 && len(webhooks) > 0 && strings.TrimSpace(getenv("X_BEARER_TOKEN")) != ""),
			Detail: fmt.Sprintf("%d user(s), flush interval %s", len(xUsers), dashboardEnvOrDefault(getenv, "X_POLL_INTERVAL", "5m")),
			Action: "Refresh or edit X_USERS / X_BEARER_TOKEN",
		},
		{
			Name:   "Polymarket notifier",
			Type:   "polymarket",
			State:  dashboardEnabledState(len(polymarketChannels) > 0 && len(webhooks) > 0),
			Detail: fmt.Sprintf("%d endpoint(s), interval %s", len(polymarketChannels), dashboardEnvOrDefault(getenv, "POLYMARKET_POLL_INTERVAL", "5m")),
			Action: "Refresh or edit POLYMARKET_CHANNELS",
		},
	}

	processState := "healthy"
	processDetail := fmt.Sprintf("sqlite %s, model %s", processCfg.SQLitePath, processCfg.OpenRouterModel)
	processAction := "POST /process-analyze"
	if len(missing) > 0 {
		processState = "disabled"
		processDetail = "missing " + strings.Join(missing, ", ")
		processAction = "Set required PROCESS_ANALYZE_* env vars"
	}
	statuses = append(statuses, dashboardSourceStatus{
		Name:   "Process + Analyze",
		Type:   "pipeline",
		State:  processState,
		Detail: processDetail,
		Action: processAction,
	})

	return statuses
}

func (rt *dashboardRuntime) proxyStatuses(snap dashboardSnapshot) []dashboardProxyStatus {
	statuses := make([]dashboardProxyStatus, 0, len(rt.proxyConfigs))
	for _, cfg := range rt.proxyConfigs {
		state := "configured"
		if strings.TrimSpace(cfg.Authorization) == "" {
			state = "warning"
		}
		statuses = append(statuses, dashboardProxyStatus{
			Name:        cfg.Name,
			RoutePrefix: cfg.RoutePrefix,
			BaseURL:     cfg.TargetBaseURL,
			AuthState:   state,
			Requests:    snap.GroupCounts["proxy:"+cfg.Name],
		})
	}
	return statuses
}

func (rt *dashboardRuntime) configStatuses() []dashboardConfigStatus {
	getenv := rt.getenv
	statuses := []dashboardConfigStatus{
		{Name: "HOST", State: dashboardConfigPresence(getenv("HOST"), false), Scope: "server", Note: "default 0.0.0.0"},
		{Name: "PORT", State: dashboardConfigPresence(getenv("PORT"), false), Scope: "server", Note: "default 8000"},
		{Name: "WEBHOOKS", State: dashboardConfigPresence(getenv("WEBHOOKS"), true), Scope: "notifiers", Note: fmt.Sprintf("%d webhook(s)", len(splitList(getenv("WEBHOOKS"))))},
		{Name: "X_BEARER_TOKEN", State: dashboardConfigPresence(getenv("X_BEARER_TOKEN"), true), Scope: "x notifier", Note: "masked in dashboard"},
	}

	for _, cfg := range rt.proxyConfigs {
		upper := strings.ToUpper(cfg.Name)
		statuses = append(statuses,
			dashboardConfigStatus{
				Name:  upper + "_API_BASE_URL",
				State: dashboardConfigPresence(getenv(upper+"_API_BASE_URL"), false),
				Scope: cfg.Name + " proxy",
				Note:  cfg.TargetBaseURL,
			},
			dashboardConfigStatus{
				Name:  upper + "_AUTHORIZATION",
				State: dashboardConfigPresence(getenv(upper+"_AUTHORIZATION"), true),
				Scope: cfg.Name + " proxy",
				Note:  "client Authorization is always stripped",
			},
		)
	}

	processCfg, missing := processanalyze.LoadConfigFromEnv(getenv)
	processState := "configured"
	processNote := processCfg.SQLitePath
	if len(missing) > 0 {
		processState = "missing"
		processNote = "missing " + strings.Join(missing, ", ")
	}
	statuses = append(statuses,
		dashboardConfigStatus{Name: "PROCESS_ANALYZE_SQLITE_PATH", State: dashboardConfigPresence(getenv("PROCESS_ANALYZE_SQLITE_PATH"), false), Scope: "process-analyze", Note: processCfg.SQLitePath},
		dashboardConfigStatus{Name: "PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION", State: dashboardConfigPresence(getenv("PROCESS_ANALYZE_OPENROUTER_AUTHORIZATION"), true), Scope: "process-analyze", Note: "required for analysis"},
		dashboardConfigStatus{Name: "PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN", State: dashboardConfigPresence(getenv("PROCESS_ANALYZE_TELEGRAM_BOT_TOKEN"), true), Scope: "process-analyze", Note: processNote},
		dashboardConfigStatus{Name: "PROCESS_ANALYZE_STATUS", State: processState, Scope: "process-analyze", Note: processNote},
	)

	return statuses
}

func (rt *dashboardRuntime) proxyRequestTotal(snap dashboardSnapshot) int {
	total := 0
	for _, cfg := range rt.proxyConfigs {
		total += snap.GroupCounts["proxy:"+cfg.Name]
	}
	return total
}

func dashboardEnabledState(enabled bool) string {
	if enabled {
		return "healthy"
	}
	return "disabled"
}

func dashboardConfigPresence(value string, secret bool) string {
	if strings.TrimSpace(value) == "" {
		if secret {
			return "missing"
		}
		return "warning"
	}
	return "configured"
}

func dashboardEnvOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func stateBadge(state string) string {
	class := "state-unknown"
	switch state {
	case "healthy", "configured":
		class = "state-healthy"
	case "warning":
		class = "state-warning"
	case "disabled", "missing":
		class = "state-disabled"
	case "degraded":
		class = "state-degraded"
	}
	return `<span class="state ` + class + `">` + html.EscapeString(strings.ToUpper(state)) + `</span>`
}

type dashboardNewsView struct {
	Type    string
	Page    int
	PerPage int
}

type dashboardNewsItem struct {
	ID        string
	Type      string
	Source    string
	Action    string
	Text      string
	Metadata  string
	CreatedAt string
	UpdatedAt string
}

type dashboardNewsPage struct {
	Items      []dashboardNewsItem
	Total      int
	Page       int
	PerPage    int
	TotalPages int
	Start      int
	End        int
	Type       string
	State      string
	Detail     string
	Path       string
	Table      string
}

func defaultDashboardNewsView() dashboardNewsView {
	return dashboardNewsView{Type: "all", Page: 1, PerPage: 10}
}

func parseDashboardNewsView(values url.Values) dashboardNewsView {
	view := defaultDashboardNewsView()
	view.Type = normalizeDashboardNewsType(values.Get("type"))
	view.Page = parseDashboardNewsInt(values.Get("page"), 1)
	view.PerPage = normalizeDashboardNewsPerPage(values.Get("per_page"))
	return view
}

func normalizeDashboardNewsView(view dashboardNewsView) dashboardNewsView {
	view.Type = normalizeDashboardNewsType(view.Type)
	view.Page = parseDashboardNewsInt(strconv.Itoa(view.Page), 1)
	view.PerPage = normalizeDashboardNewsPerPage(strconv.Itoa(view.PerPage))
	return view
}

func normalizeDashboardNewsType(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "all":
		return "all"
	case "telegram", "x", "polymarket":
		return raw
	default:
		return "all"
	}
}

func normalizeDashboardNewsPerPage(raw string) int {
	value := parseDashboardNewsInt(raw, defaultDashboardNewsView().PerPage)
	switch value {
	case 2, 5, 10, 20, 50:
		return value
	default:
		return defaultDashboardNewsView().PerPage
	}
}

func parseDashboardNewsInt(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func (v dashboardNewsView) withType(value string) dashboardNewsView {
	v.Type = value
	v.Page = 1
	return v
}

func (v dashboardNewsView) withPage(value int) dashboardNewsView {
	v.Page = value
	return v
}

func (v dashboardNewsView) withPerPage(value int) dashboardNewsView {
	v.PerPage = value
	v.Page = 1
	return v
}

func dashboardNewsURL(view dashboardNewsView) string {
	base := "/dashboard/partials/news"
	values := url.Values{}
	if view.Type != "" && view.Type != "all" {
		values.Set("type", view.Type)
	}
	if view.Page > 1 {
		values.Set("page", strconv.Itoa(view.Page))
	}
	if view.PerPage > 0 && view.PerPage != defaultDashboardNewsView().PerPage {
		values.Set("per_page", strconv.Itoa(view.PerPage))
	}
	if query := values.Encode(); query != "" {
		return base + "?" + query
	}
	return base
}

func (rt *dashboardRuntime) newsPage(view dashboardNewsView) dashboardNewsPage {
	cfg, missing := processanalyze.LoadConfigFromEnv(rt.getenv)
	page := dashboardNewsPage{
		Page:    view.Page,
		PerPage: view.PerPage,
		Type:    view.Type,
		State:   "warning",
		Detail:  "n/a",
		Path:    cfg.SQLitePath,
		Table:   cfg.SQLiteTable,
	}
	if len(missing) > 0 {
		page.State = "disabled"
		page.Detail = "process-analyze missing " + strings.Join(missing, ", ")
		return page
	}

	path := strings.TrimSpace(cfg.SQLitePath)
	if path == "" {
		page.State = "disabled"
		page.Detail = "sqlite path is empty"
		return page
	}
	if path == ":memory:" {
		page.Detail = "in-memory sqlite has no persistent file metrics"
		return page
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			page.Detail = "sqlite file not created yet"
			return page
		}
		page.State = "degraded"
		page.Detail = "stat sqlite file: " + err.Error()
		return page
	}
	_ = info

	if !dashboardSQLiteIdentifierPattern.MatchString(cfg.SQLiteTable) {
		page.State = "degraded"
		page.Detail = fmt.Sprintf("invalid sqlite table name %q", cfg.SQLiteTable)
		return page
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		page.State = "degraded"
		page.Detail = "open sqlite db: " + err.Error()
		return page
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		page.State = "degraded"
		page.Detail = "ping sqlite db: " + err.Error()
		return page
	}

	where := []string{"1=1"}
	args := make([]any, 0, 4)
	if page.Type != "all" {
		where = append(where, "type = ?")
		args = append(args, page.Type)
	}
	whereSQL := strings.Join(where, " AND ")

	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, cfg.SQLiteTable, whereSQL)
	if err := db.QueryRow(countQuery, args...).Scan(&page.Total); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			page.Detail = "sqlite table not created yet"
			return page
		}
		page.State = "degraded"
		page.Detail = "count sqlite rows: " + err.Error()
		return page
	}

	page.TotalPages = 1
	if page.Total > 0 {
		page.TotalPages = (page.Total + page.PerPage - 1) / page.PerPage
	}
	if page.Page > page.TotalPages {
		page.Page = page.TotalPages
	}
	if page.Page < 1 {
		page.Page = 1
	}
	if page.PerPage < 1 {
		page.PerPage = defaultDashboardNewsView().PerPage
	}

	start := 0
	end := 0
	if page.Total > 0 {
		start = (page.Page - 1) * page.PerPage
		if start > page.Total {
			start = page.Total
		}
		end = start + page.PerPage
		if end > page.Total {
			end = page.Total
		}
	}
	page.Start = 0
	page.End = 0
	if end > start {
		page.Start = start + 1
		page.End = end
	}

	limitQuery := fmt.Sprintf(`SELECT id, text, type, source, metadata, action, created_at, updated_at FROM %s WHERE %s ORDER BY CAST(updated_at AS INTEGER) DESC, CAST(created_at AS INTEGER) DESC, id DESC LIMIT ? OFFSET ?`, cfg.SQLiteTable, whereSQL)
	rows, err := db.Query(limitQuery, append(args, page.PerPage, start)...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			page.Detail = "sqlite table not created yet"
			return page
		}
		page.State = "degraded"
		page.Detail = "load sqlite rows: " + err.Error()
		return page
	}
	defer rows.Close()

	for rows.Next() {
		var item dashboardNewsItem
		if err := rows.Scan(&item.ID, &item.Text, &item.Type, &item.Source, &item.Metadata, &item.Action, &item.CreatedAt, &item.UpdatedAt); err != nil {
			page.State = "degraded"
			page.Detail = "scan sqlite row: " + err.Error()
			page.Items = nil
			return page
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		page.State = "degraded"
		page.Detail = "iterate sqlite rows: " + err.Error()
		page.Items = nil
		return page
	}

	if page.Total == 0 {
		page.State = "healthy"
		page.Detail = "sqlite accessible; no news rows yet"
		return page
	}
	page.State = "healthy"
	page.Detail = fmt.Sprintf("sqlite accessible; %d row(s)", page.Total)
	return page
}

func renderNewsSection(rt *dashboardRuntime, view dashboardNewsView) string {
	page := rt.newsPage(normalizeDashboardNewsView(view))

	var rows strings.Builder
	if len(page.Items) == 0 {
		rows.WriteString(`<tr><td colspan="7" class="subtle">No news rows match the current filters.</td></tr>`)
	} else {
		for _, item := range page.Items {
			action := strings.TrimSpace(item.Action)
			if action == "" {
				action = "-"
			}
			fmt.Fprintf(&rows, `<tr><td class="mono">%s</td><td class="mono">%s</td><td>%s</td><td class="mono">%s</td><td class="mono"><a href="%s" target="_blank" rel="noreferrer">%s</a></td><td>%s</td><td class="mono">%s</td></tr>`,
				html.EscapeString(item.ID),
				html.EscapeString(formatDashboardUnixMillis(item.UpdatedAt)),
				html.EscapeString(strings.TrimSpace(item.Type)),
				html.EscapeString(action),
				html.EscapeString(item.Source),
				html.EscapeString(item.Source),
				html.EscapeString(item.Text),
				html.EscapeString(item.Metadata),
			)
		}
	}

	var typeButtons strings.Builder
	for _, option := range []struct{ Value, Label string }{
		{Value: "all", Label: "All"},
		{Value: "telegram", Label: "Telegram"},
		{Value: "x", Label: "x.com"},
		{Value: "polymarket", Label: "Polymarket"},
	} {
		buttonView := view.withType(option.Value)
		fmt.Fprintf(&typeButtons, `<button type="button" class="%s" data-on:click="@get('%s')">%s</button>`,
			sourcesChipClass(page.Type == option.Value),
			html.EscapeString(dashboardNewsURL(buttonView)),
			html.EscapeString(option.Label),
		)
	}

	var perPageButtons strings.Builder
	for _, option := range []int{2, 5, 10, 20, 50} {
		buttonView := view.withPerPage(option)
		fmt.Fprintf(&perPageButtons, `<button type="button" class="%s" data-on:click="@get('%s')">%d</button>`,
			sourcesChipClass(page.PerPage == option),
			html.EscapeString(dashboardNewsURL(buttonView)),
			option,
		)
	}

	showing := "No news rows match the current filters."
	if page.Total > 0 {
		showing = fmt.Sprintf("Showing %d-%d of %d news row(s) · page %d of %d", page.Start, page.End, page.Total, page.Page, page.TotalPages)
	}

	prevDisabled := page.Page <= 1 || page.Total == 0
	nextDisabled := page.Page >= page.TotalPages || page.Total == 0
	prevURL := dashboardNewsURL(view.withPage(page.Page - 1))
	nextURL := dashboardNewsURL(view.withPage(page.Page + 1))

	return `<section id="dashboard-news" class="panel" data-show="$section == 'news' || $section == 'overview'">
  <div class="panel-header">
    <h2>Stored news</h2>
    <button data-on:click="@get('/dashboard/partials/news')">Refresh</button>
  </div>
  <div class="cards">
    <div class="card"><span class="label">SQLite state</span><span class="value">` + html.EscapeString(strings.ToUpper(page.State)) + `</span><span class="subtle">` + html.EscapeString(page.Detail) + `</span></div>
    <div class="card"><span class="label">Total rows</span><span class="value">` + html.EscapeString(strconv.Itoa(page.Total)) + `</span><span class="subtle">table ` + html.EscapeString(page.Table) + `</span></div>
    <div class="card"><span class="label">Showing</span><span class="value">` + html.EscapeString(func() string {
		if page.Total == 0 {
			return "0"
		}
		return fmt.Sprintf("%d-%d", page.Start, page.End)
	}()) + `</span><span class="subtle">page ` + html.EscapeString(strconv.Itoa(page.Page)) + ` of ` + html.EscapeString(strconv.Itoa(page.TotalPages)) + `</span></div>
    <div class="card"><span class="label">Type filter</span><span class="value">` + html.EscapeString(page.Type) + `</span><span class="subtle">current scope</span></div>
  </div>
  <div class="filters">
    <div class="filter-group">
      <span class="label">Type</span>
      <div class="chips">` + typeButtons.String() + `</div>
    </div>
    <div class="filter-group">
      <span class="label">Per page</span>
      <div class="chips">` + perPageButtons.String() + `</div>
    </div>
  </div>
  <div class="footer-note">` + html.EscapeString(showing) + `</div>
  <div class="table-wrap">
    <table>
      <thead><tr><th>ID</th><th>Updated</th><th>Type</th><th>Action</th><th>Source</th><th>Text</th><th>Metadata</th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table>
  </div>
  <div class="pager">
    <div class="subtle">` + html.EscapeString(showing) + `</div>
    <div class="pager-actions">
      <button type="button" class="pager-button"` + disabledAttr(prevDisabled) + ` data-on:click="@get('` + html.EscapeString(prevURL) + `')">Prev</button>
      <button type="button" class="pager-button"` + disabledAttr(nextDisabled) + ` data-on:click="@get('` + html.EscapeString(nextURL) + `')">Next</button>
    </div>
  </div>
</section>`
}

func formatDashboardUnixMillis(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "n/a"
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return raw
	}
	if n <= 0 {
		return raw
	}
	return time.UnixMilli(n).UTC().Format(time.RFC3339)
}
