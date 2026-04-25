#!/usr/bin/env bash
# tg-channel-to-rss — example curl requests.
#
# Usage:
#   ./examples/requests.sh              # run all examples against localhost:8000
#   HOST=http://example.com ./examples/requests.sh feed proxy-bybit
#
# Each group is implemented as a function; pass group names as args to run a
# subset. No args runs every group.

set -u
HOST="${HOST:-http://localhost:8000}"

hr() { printf '\n\033[1;34m── %s ──\033[0m\n' "$*"; }
req() {
  local method="$1"; shift
  local url="$1"; shift
  printf '\033[1;32m$ curl -sS -X %s %s%s\033[0m\n' "$method" "$url" " $*"
  # shellcheck disable=SC2086
  curl -sS -o >(sed 's/^/   /') -w '   → HTTP %{http_code}\n' -X "$method" "$url" "$@" || true
}

feed() {
  hr "feed: happy path"
  req GET "$HOST/feed/durov"

  hr "feed: missing channel name (400)"
  req GET "$HOST/feed/"

  hr "feed: invalid channel name (400)"
  req GET "$HOST/feed/ab"
  req GET "$HOST/feed/hello-world"

  hr "feed: unknown channel (400 Telegram channel not found)"
  req GET "$HOST/feed/this_channel_does_not_exist_0001"

  hr "feed: unknown path (404)"
  req GET "$HOST/unknown"
}

proxy_hyperliquid() {
  hr "hyperliquid: POST /info type=meta"
  req POST "$HOST/proxy/hyperliquid/info" \
    -H 'Content-Type: application/json' \
    -d '{"type":"meta"}'

  hr "hyperliquid: POST /info type=allMids"
  req POST "$HOST/proxy/hyperliquid/info" \
    -H 'Content-Type: application/json' \
    -d '{"type":"allMids"}'

  hr "hyperliquid: client-side Authorization is stripped server-side"
  req POST "$HOST/proxy/hyperliquid/info" \
    -H 'Authorization: Bearer will-be-stripped' \
    -H 'Content-Type: application/json' \
    -d '{"type":"meta"}'
}

proxy_polymarket() {
  hr "polymarket: list markets"
  req GET "$HOST/proxy/polymarket/markets"

  hr "polymarket: markets with pagination params"
  req GET "$HOST/proxy/polymarket/markets?next_cursor=MA%3D%3D&limit=10"
}


process_analyze() {
  hr "process-analyze: notifier payload -> Sheets + OpenRouter + Telegram"
  req POST "$HOST/process-analyze"     -H 'Content-Type: application/json'     -d '{"id":"delivery-1","source_type":"telegram","source_url":"https://t.me/s/durov","channel":"durov","item":{"title":"New post in channel @durov","description":"<p>hello</p>","link":"https://t.me/s/durov/1","created":"2026-04-25T10:00:00Z","id":"https://t.me/s/durov/1","content":"Some headline [link] body","metadata":{"views":"1234"}}}'
}

proxy_bybit() {
  hr "bybit: server time"
  req GET "$HOST/proxy/bybit/v5/market/time"

  hr "bybit: linear tickers for BTCUSDT"
  req GET "$HOST/proxy/bybit/v5/market/tickers?category=linear&symbol=BTCUSDT"

  hr "bybit: 60-minute klines, limit 5"
  req GET "$HOST/proxy/bybit/v5/market/kline?category=linear&symbol=BTCUSDT&interval=60&limit=5"

  hr "bybit: orderbook depth"
  req GET "$HOST/proxy/bybit/v5/market/orderbook?category=linear&symbol=BTCUSDT&limit=25"
}

all=(feed proxy-hyperliquid proxy-polymarket proxy-bybit process-analyze)

run() {
  case "$1" in
    feed)             feed ;;
    proxy-hyperliquid) proxy_hyperliquid ;;
    proxy-polymarket)  proxy_polymarket ;;
    proxy-bybit)       proxy_bybit ;;
    process-analyze)    process_analyze ;;
    *) echo "unknown group: $1 (available: ${all[*]})" >&2; return 2 ;;
  esac
}

if [ "$#" -eq 0 ]; then
  set -- "${all[@]}"
fi

for group in "$@"; do
  run "$group"
done
