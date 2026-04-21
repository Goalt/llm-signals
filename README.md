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
