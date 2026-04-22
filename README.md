# tg-channel-to-rss

Go service for converting a **public Telegram channel** into a **JSON feed**.

## How it works
1. The service receives HTTP requests:
   `GET /feed/{channel_name}`
2. It fetches the public static view of the channel at
   `https://t.me/s/{channel_name}`.
3. It parses Telegram message bubbles and extracts:
   - Post text,
   - Photo preview images,
   - Publication time and post URL.
4. The extracted data is returned as JSON.

⚠ **Limitations**
- Telegram **does not guarantee** that all public channels expose their posts on `t.me/s/…`.
- Channels flagged as **sensitive**, geo-restricted, or with **content protection** enabled may show a blank page or limited content.

## Requirements
- Go 1.24+
- Docker

## Run locally

### 1. Скопируйте пример конфигурации

```bash
cp .env.example .env
```

Файл [.env.example](.env.example) описывает все переменные окружения сервиса.
Отредактируйте `.env` и оставьте пустыми те блоки, которые не используете —
нотификаторы и прокси активируются только при наличии соответствующих значений.

### 2. Запустите сервер

Минимальный запуск (только HTTP-фид, без нотификаторов):

```bash
go run ./cmd/server
```

С загрузкой `.env` (bash / zsh):

```bash
set -a && source .env && set +a && go run ./cmd/server
```

Альтернативно, через [`godotenv`](https://github.com/joho/godotenv/tree/master/cmd/godotenv):

```bash
go install github.com/joho/godotenv/cmd/godotenv@latest
godotenv -f .env go run ./cmd/server
```

Сервер выведет в лог статус нотификаторов и адрес прослушивания:

```
notifier disabled: set TG_CHANNELS and WEBHOOKS to enable
x.com notifier disabled: set X_USERS and WEBHOOKS to enable
Serving tg-channel-to-rss on http://0.0.0.0:8000
```

### 3. Проверьте, что фид работает

```bash
curl 'http://localhost:8000/feed/durov' | jq '.title, (.items | length)'
```

Ожидаемый результат — `"Pavel Durov – Telegram"` и число постов > 0.

Несуществующий канал возвращает `400 Telegram channel not found`:

```bash
curl -i 'http://localhost:8000/feed/nonexistent_channel_xyz'
# HTTP/1.1 400 Bad Request
# Telegram channel not found
```

### 4. (Опционально) Включите нотификаторы

- **Telegram → вебхуки:** заполните в `.env` `TG_CHANNELS=durov,telegram` и `WEBHOOKS=https://hooks.example.com/tg`.
- **x.com → вебхуки:** дополнительно к `WEBHOOKS` заполните `X_USERS=elonmusk` и `X_BEARER_TOKEN=<bearer>` (токен получить на developer.x.com).

Перезапустите сервер и убедитесь, что в логе вместо `disabled` появляется:

```
notifier: polling 2 channel(s) every 5m0s, dispatching to 1 webhook(s)
x.com notifier: polling 1 user(s) every 5m0s, dispatching to 1 webhook(s)
```

### 5. (Опционально) Запустите тесты

```bash
# Быстрый регресс (unit + black-box):
go test ./... -count=1

# Manual HTTP — требует запущенного сервера:
bash tests/manual/run.sh        # лог: tests/manual/run.log

# Нотификаторы — без внешних зависимостей:
go run ./tests/manual/notifier_e2e
bash tests/manual/notifier-wiring.sh
```

Полный 7-слойный регресс описан в [.github/agents/api-integrator.agent.md](.github/agents/api-integrator.agent.md)
и автоматизирован в [.github/workflows/agent-regression.yml](.github/workflows/agent-regression.yml).

## Build and run with Docker
1. Build image:
```bash
docker build -t tg-channel-to-rss .
```
2. Run container:
```bash
docker run --rm -p 8000:8000 tg-channel-to-rss
```

## Usage
```bash
curl 'http://localhost:8000/feed/cool_telegram_channel'
```

## API proxy endpoints

The service also exposes server-side HTTP proxies for Hyperliquid, Polymarket,
and Bybit API calls:

- `/proxy/hyperliquid/...` → forwards to `HYPERLIQUID_API_BASE_URL`
- `/proxy/polymarket/...` → forwards to `POLYMARKET_API_BASE_URL`
- `/proxy/bybit/...` → forwards to `BYBIT_API_BASE_URL`

Examples:

```bash
curl 'http://localhost:8000/proxy/hyperliquid/info'
curl 'http://localhost:8000/proxy/polymarket/markets'
curl 'http://localhost:8000/proxy/bybit/v5/market/tickers?category=linear'
```

When configured, the server injects `Authorization` headers for upstream
requests using environment variables. Client-provided `Authorization` headers
are ignored.

## Optional environment variables
- `PORT` (default `8000`): HTTP listening port.
- `HOST` (default `0.0.0.0`): HTTP bind address.
- `HYPERLIQUID_API_BASE_URL` (default `https://api.hyperliquid.xyz`): upstream base URL for Hyperliquid proxy.
- `HYPERLIQUID_AUTHORIZATION` (optional): `Authorization` header value injected for Hyperliquid upstream requests.
- `POLYMARKET_API_BASE_URL` (default `https://clob.polymarket.com`): upstream base URL for Polymarket proxy.
- `POLYMARKET_AUTHORIZATION` (optional): `Authorization` header value injected for Polymarket upstream requests.
- `BYBIT_API_BASE_URL` (default `https://api.bybit.com`): upstream base URL for Bybit proxy.
- `BYBIT_AUTHORIZATION` (optional): `Authorization` header value injected for Bybit upstream requests.

## Notifier module

In addition to serving the JSON feed over HTTP, the server can periodically
collect the latest posts from a list of Telegram channels and forward each
new post to a list of webhooks. The notifier runs in-process alongside the
HTTP server and is enabled automatically when `TG_CHANNELS` and `WEBHOOKS`
are set.

### Run with notifier enabled
```bash
TG_CHANNELS=channel_a,channel_b \
WEBHOOKS=https://example.com/hook1,https://example.com/hook2 \
POLL_INTERVAL=5m \
go run ./cmd/server
```

### Environment variables
- `TG_CHANNELS` (optional): comma-separated list of public Telegram channel names. Required to enable the notifier.
- `WEBHOOKS` (optional): comma-separated list of webhook URLs that will receive new posts. Required to enable the notifier.
- `POLL_INTERVAL` (optional, default `5m`): polling interval as a Go duration (e.g. `30s`, `10m`, `1h`).

## x.com notifier module

The same webhook payload can also be produced from x.com posts using the
official x.com API. This notifier runs in parallel with the Telegram notifier
when configured.

### Run with x.com notifier enabled
```bash
X_USERS=jack,github \
X_BEARER_TOKEN=your_x_api_bearer_token \
WEBHOOKS=https://example.com/hook1,https://example.com/hook2 \
X_POLL_INTERVAL=5m \
go run ./cmd/server
```

### Environment variables
- `X_USERS` (optional): comma-separated list of x.com usernames to poll.
- `X_BEARER_TOKEN` (required when `X_USERS` is set): x.com API bearer token.
- `WEBHOOKS` (required): comma-separated webhook URLs (shared with Telegram notifier).
- `X_POLL_INTERVAL` (optional, default `5m`): polling interval as a Go duration.

On startup the notifier performs a seed pass that records currently
visible posts as "already seen" so subscribers are not flooded with
historical messages. Each subsequent poll delivers a JSON payload per
new post to every configured webhook:

```json
{
  "channel": "channel_a",
  "item": { "title": "...", "link": "...", "created": "...", "id": "...", "content": "..." }
}
```

## RSS/Atom notifier module

The same webhook payload can also be produced from any standard RSS 2.0 or
Atom feed. This notifier runs in parallel with the Telegram and x.com
notifiers when configured.

### Run with RSS notifier enabled
```bash
RSS_FEEDS=https://example.com/rss,https://blog.example.com/atom \
WEBHOOKS=https://example.com/hook1 \
RSS_POLL_INTERVAL=5m \
go run ./cmd/server
```

### Environment variables
- `RSS_FEEDS` (optional): comma-separated list of full `http(s)` URLs of RSS 2.0 or Atom feeds. Required to enable the RSS notifier.
- `WEBHOOKS` (required): comma-separated webhook URLs (shared with the other notifiers).
- `RSS_POLL_INTERVAL` (optional, default `5m`): polling interval as a Go duration.
- `RSS_MIN_REQUEST_INTERVAL` (optional, default `1s`): minimum spacing between outbound RSS HTTP requests. `0` disables throttling.

The `channel` field in the webhook payload contains the feed URL that produced
the item, so downstream consumers can correlate posts with their source.
