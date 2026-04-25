# Copilot instructions for this repository

## Build, test, and static-check commands

Use the Go toolchain directly; there is no Makefile or dedicated lint config in this repo.

```bash
# Build everything
go build ./...

# Static checks used in CI
go vet ./...

# Full regression used in README
go test ./... -count=1
```

For targeted runs, prefer the narrowest package:

```bash
# Single package
go test ./internal/app -count=1
go test ./tests -count=1

# Single test
go test ./internal/app -run TestGetJSONFeedSuccess -count=1
go test ./tests -run TestNotifier_EndToEnd_DispatchesOnlyNewItemsToAllWebhooks -count=1

# Optional credentials-gated e2e layer
go test -tags=e2e ./... -count=1
```

Manual regression scripts are part of the intended workflow:

```bash
# HTTP regression against a running local server on :8000
go run ./cmd/server
bash tests/manual/run.sh

# Notifier wiring regression expects a built binary at /tmp/tg-server
go build -o /tmp/tg-server ./cmd/server
bash tests/manual/notifier-wiring.sh

# Notifier end-to-end driver
go run ./tests/manual/notifier_e2e
```

## High-level architecture

The main entrypoint is `cmd/server/main.go`. One process hosts four surfaces:

1. `GET /feed/{channel}` uses `internal/app.Service` to fetch `https://t.me/s/{channel}`, parse Telegram HTML with `goquery`, and return a normalized `app.FeedJSON`.
2. `/proxy/hyperliquid/*`, `/proxy/polymarket/*`, and `/proxy/bybit/*` are thin reverse proxies created in `cmd/server/proxy.go`.
3. `/mcp` exposes the same data through JSON-RPC/MCP over HTTP, while `go run ./cmd/server mcp` exposes the same MCP server over stdio.
4. Background notifiers start from `main.go` when the right env vars are present and reuse feed-producing services to push new items to webhooks.

`internal/app` defines the core feed shape (`FeedJSON`, `FeedItemJSON`) that other integrations mirror. `internal/polymarket` and `internal/xapi` both translate upstream APIs into that same feed shape so they can plug into the shared notifier. `internal/notifier` is source-agnostic: it polls any `FeedFetcher`, seeds a seen-set on startup, then dispatches only newly discovered items to every configured webhook.

The x.com path is the least obvious part of the system: `startXNotifier` opens one long-lived filtered stream via `internal/xapi/stream.go`, buffers tweets in memory, and later flushes those buffered items through the shared notifier loop. It does not do a fresh API fetch per poll once streaming is active.

## Key repository-specific conventions

- The repo directory is `llm-signals`, but the module, binary logs, user agent strings, and README still use `tg-channel-to-rss`. Keep that naming consistent unless the change is intentionally repo-wide.
- Feature enablement is environment-driven and usually requires a pair of lists: Telegram notifier needs `TG_CHANNELS` + `WEBHOOKS`, x.com needs `X_USERS` + `WEBHOOKS` + `X_BEARER_TOKEN`, and Polymarket needs `POLYMARKET_CHANNELS` + `WEBHOOKS`. Poll intervals use Go duration strings.
- New upstream HTTP integrations should follow the existing split: proxy wiring in `cmd/server/main.go`, generic reverse-proxy behavior in `cmd/server/proxy.go`, and any feed-producing domain client in `internal/<name>/service.go`.
- Proxies must strip any client-supplied `Authorization` header and inject only the server-side `*_AUTHORIZATION` value. This behavior is explicitly tested and should not be weakened.
- Feed-producing services should preserve stable item identity in `FeedItemJSON.ID` (falling back to `Link` when needed) because notifier deduplication depends on it.
- Source-specific extra fields belong in `FeedItemJSON.Metadata` instead of new top-level feed fields. Existing integrations use metadata to pass through raw upstream context to webhook consumers.
- Tests are intentionally layered:
  - colocated unit tests under `cmd/server` and `internal/**`
  - higher-level black-box tests under `tests/`
  - manual shell regressions under `tests/manual/`
  - optional build-tagged e2e tests with `-tags=e2e`
- When adding routes or integrations, keep the manual regression assets in sync: `tests/manual/run.sh` covers live HTTP paths, and notifier-related env handling is checked by `tests/manual/notifier-wiring.sh`.
- MCP changes usually need updates in more than one place: `internal/mcp/server.go` tool definitions/dispatch, `cmd/server/mcp.go` or HTTP wiring, and the README MCP examples.
