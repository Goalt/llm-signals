# tg-channel-to-rss
Dockerized service (with AWS Lambda-compatible handler) for converting a **public Telegram channel** into an **RSS feed**.

## How it works
1. The service receives HTTP requests:
   `GET /feed/{channel_name}?key={api_key}`
2. It fetches the public static view of the channel at
   `https://t.me/s/{channel_name}`.
3. Using **BeautifulSoup**, it parses each Telegram message bubble, extracts:
   - Post text (with links preserved),
   - Photo previews and link-preview images,
   - Publication time and post URL.
4. The extracted data is converted into an RSS feed with [rfeed](https://pypi.org/project/rfeed/), returning valid XML to the caller.

⚠ **Limitations**
- Telegram **does not guarantee** that all public channels expose their posts on `t.me/s/…`.
- Channels flagged as **sensitive**, geo-restricted, or with **content protection** enabled may show a blank page or limited content even though they are public in the Telegram app.
- There is no workaround other than viewing those channels within Telegram or using the official Bot API.

## Requirements
- Python 3.13 or higher
- Docker

## Build and run with Docker
1. Build image:
```bash
docker build -t tg-channel-to-rss .
```
2. Run container:
```bash
docker run --rm -p 8000:8000 -e API_KEY=YOUR_KEY tg-channel-to-rss
```

## Usage
Call the endpoint with the channel name and your API key:
```bash
curl 'http://localhost:8000/feed/cool_telegram_channel?key=YOUR_KEY'
```
This returns an RSS XML feed of the channel’s recent posts, including text and photo previews, ready to import into your RSS reader.

## Optional environment variables
- `API_KEY` (required): shared key for request authentication.
- `PORT` (optional, default `8000`): HTTP listening port inside the container.
- `HOST` (optional, default `0.0.0.0`): HTTP bind address.
