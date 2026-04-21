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
```bash
go run ./cmd/server
```

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

## Optional environment variables
- `PORT` (default `8000`): HTTP listening port.
- `HOST` (default `0.0.0.0`): HTTP bind address.

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
