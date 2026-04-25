#!/usr/bin/env bash
set -u
HOST="${HOST:-http://localhost:8000}"
LOG="${LOG:-tests/manual/run.log}"
mkdir -p "$(dirname "$LOG")"
: > "$LOG"

hit() {
  local name="$1" method="$2" url="$3"; shift 3
  {
    echo "=== $name"
    echo "\$ curl -sS -X $method '$url' $*"
  } >> "$LOG"
  curl -sS -o /tmp/body -D /tmp/hdr \
    -w "HTTP %{http_code}  time=%{time_total}s  size=%{size_download}B\n" \
    -X "$method" "$url" "$@" >> "$LOG" 2>&1
  {
    echo "--- response headers:"
    sed -n '1,12p' /tmp/hdr
    echo "--- response body (first 400B):"
    head -c 400 /tmp/body
    echo
    echo
  } >> "$LOG"
}

hit "feed missing"         GET  "$HOST/feed/"
hit "feed short"           GET  "$HOST/feed/ab"
hit "feed bad chars"       GET  "$HOST/feed/hello-world"
hit "feed 33 chars"        GET  "$HOST/feed/$(printf 'a%.0s' {1..33})"
hit "feed not found"       GET  "$HOST/feed/this_channel_does_not_exist_0001"
hit "unknown path"         GET  "$HOST/unknown"
hit "feed ok durov"        GET  "$HOST/feed/durov"
hit "feed ok telegram"     GET  "$HOST/feed/telegram"

hit "hl info meta"         POST "$HOST/proxy/hyperliquid/info" -H 'Content-Type: application/json' -d '{"type":"meta"}'
hit "hl info allMids"      POST "$HOST/proxy/hyperliquid/info" -H 'Content-Type: application/json' -d '{"type":"allMids"}'
hit "hl auth stripped"     POST "$HOST/proxy/hyperliquid/info" -H 'Authorization: Bearer leak' -H 'Content-Type: application/json' -d '{"type":"meta"}'

hit "pm markets"           GET  "$HOST/proxy/polymarket/markets"
hit "pm markets paginated" GET  "$HOST/proxy/polymarket/markets?next_cursor=MA%3D%3D&limit=10"

hit "bybit time"           GET  "$HOST/proxy/bybit/v5/market/time"
hit "bybit tickers"        GET  "$HOST/proxy/bybit/v5/market/tickers?category=linear&symbol=BTCUSDT"
hit "bybit kline"          GET  "$HOST/proxy/bybit/v5/market/kline?category=linear&symbol=BTCUSDT&interval=60&limit=5"
hit "bybit orderbook"      GET  "$HOST/proxy/bybit/v5/market/orderbook?category=linear&symbol=BTCUSDT&limit=25"

hit "process analyze"      POST "$HOST/process-analyze" -H 'Content-Type: application/json' -d '{"id":"delivery-1","source_type":"telegram","source_url":"https://t.me/s/durov","channel":"durov","item":{"title":"New post in channel @durov","description":"<p>hello</p>","link":"https://t.me/s/durov/1","created":"2026-04-25T10:00:00Z","id":"https://t.me/s/durov/1","content":"Some headline [link] body","metadata":{"views":"1234"}}}'

echo "log: $LOG  ($(wc -l < "$LOG") lines)"
