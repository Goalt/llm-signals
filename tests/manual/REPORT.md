# Manual test report — tg-channel-to-rss

- **Дата:** 2026-04-21 20:30 UTC
- **Ревизия:** `master` @ Goalt/llm-signals
- **Сервер:** `/tmp/tg-server` (собран из [cmd/server](../../cmd/server)), слушает `http://localhost:8000`
- **Источник примеров:** [examples/requests.http](../../examples/requests.http), [examples/requests.sh](../../examples/requests.sh)
- **Скрипт прогона:** [tests/manual/run.sh](run.sh) → полный лог: [tests/manual/run.log](run.log)
- **Нотификаторы:** выключены (`TG_CHANNELS` / `X_USERS` / `WEBHOOKS` не заданы).

## Сводка

| # | Endpoint | Ожидание | Факт | Вердикт |
|---|---|---|---|---|
| 1 | `GET /feed/` | 400 `Missing channel_name` | 400, body `Missing channel_name`, `Content-Type: text/plain; charset=UTF-8` | ✅ |
| 2 | `GET /feed/ab` | 400 `Invalid channel_name` | 400, body `Invalid channel_name` | ✅ |
| 3 | `GET /feed/hello-world` | 400 `Invalid channel_name` | 400, body `Invalid channel_name` | ✅ |
| 4 | `GET /feed/aaaa…aaa` (33 chars) | 400 `Invalid channel_name` | 400, body `Invalid channel_name` | ✅ |
| 5 | `GET /feed/this_channel_does_not_exist_0001` | 400 `Telegram channel not found` | **200 OK, пустые items** | ⚠ баг/лимитация |
| 6 | `GET /unknown` | 404 `Not Found` | 404, body `Not Found` | ✅ |
| 7 | `GET /feed/durov` | 200, валидный JSON фид | 200, 66 225 B, `Cache-Control: max-age=60, public`, items с контентом | ✅ |
| 8 | `GET /feed/telegram` | 200, валидный JSON фид | 200, 72 353 B, title `Telegram News – Telegram` | ✅ |
| 9 | `POST /proxy/hyperliquid/info` `{type:"meta"}` | 200, JSON с `universe` | 200, 17 041 B, включает `{"universe":[…BTC…ETH…]}` | ✅ |
| 10 | `POST /proxy/hyperliquid/info` `{type:"allMids"}` | 200, mapping цен | 200, 9 225 B, корректный JSON | ✅ |
| 11 | `POST /proxy/hyperliquid/info` с клиентским `Authorization: Bearer leak` | Клиентский заголовок вырезается, upstream отвечает нормально | 200, тело идентично п. 9 → заголовок не прокинут (иначе upstream бы отверг) | ✅ |
| 12 | `GET /proxy/polymarket/markets` | 200, JSON c `data[]` | 200, 1 823 307 B, `Cf-Cache-Status: HIT` | ✅ |
| 13 | `GET /proxy/polymarket/markets?next_cursor=MA%3D%3D&limit=10` | 200, passthrough query | 200, `Cf-Cache-Status: EXPIRED` (доказывает отдельный запрос с другими параметрами) | ✅ |
| 14 | `GET /proxy/bybit/v5/market/time` | 200, `retCode:0` | 200, `{"retCode":0,…"time":1776803461354}` | ✅ |
| 15 | `GET /proxy/bybit/v5/market/tickers?category=linear&symbol=BTCUSDT` | 200, `result.list[0].symbol=BTCUSDT` | 200, `lastPrice=75641.60`, `retCode:0` | ✅ |
| 16 | `GET /proxy/bybit/v5/market/kline?…interval=60&limit=5` | 200, 5 свечей | 200, `list` содержит OHLCV-массивы | ✅ |
| 17 | `GET /proxy/bybit/v5/market/orderbook?…limit=25` | 200, `result.b` / `result.a` | 200, `"b":[["75643.2","7.4"],…]` | ✅ |

**Итог:** 16/17 ✅, 1 ⚠ (см. ниже).

## Замечания

### ⚠ Несуществующий Telegram-канал отдаёт 200 с пустым фидом

**Пример 5:** `GET /feed/this_channel_does_not_exist_0001` вернул:

```http
HTTP/1.1 200 OK
Cache-Control: max-age=60, public
Content-Type: application/json; charset=UTF-8
```

```json
{
  "title": "Telegram: Contact @this_channel_does_not_exist_0001",
  "link":  "https://t.me/s/this_channel_does_not_exist_0001",
  "description": "Posts from Telegram: Contact @this_channel_does_not_exist_0001",
  "created": "2026-04-21T20:30:59.639400093Z",
  "items": []
}
```

**Причина:** `t.me/s/<unknown>` отвечает HTTP 200 страницей «Contact @…», поэтому проверка `res.StatusCode != 200` в [internal/app/service.go](../../internal/app/service.go#L141) не срабатывает — документ парсится, в нём нет `tgme_widget_message_bubble`, и мы отдаём пустой фид.

**Противоречит** README ("Telegram channel not found") и поведению, задокументированному в [internal/app/service_test.go](../../internal/app/service_test.go#L75) — тест это покрывает только моком, возвращающим 404.

**Возможные фиксы** (на выбор, не реализованы — требуется решение):

1. После парсинга проверять: если `doc.Find("div.tgme_channel_info").Length() == 0` и в body присутствует маркер «Contact @…» — возвращать ошибку «Telegram channel not found».
2. Проверять наличие `<meta property="og:type" content="profile">` / отсутствие `og:type=website` + отсутствие `.tgme_channel_info` → трактовать как not-found.
3. Если `items == 0` и `title` начинается с `Telegram: Contact @` — возвращать 404 или ошибку с текстом из README.

### ℹ Прокси корректно глушит клиентский `Authorization`

В примере 11 upstream Hyperliquid ответил идентично запросу без заголовка (пример 9). Если бы прокси проксировал клиентский `Authorization: Bearer leak` — CloudFront / API-gateway с большой вероятностью отдали бы 401/403 или игнор; получен стандартный 200 с `universe`. Это косвенно подтверждает логику в [cmd/server/proxy.go](../../cmd/server/proxy.go#L44-L48).

### ℹ `Cache-Control` отдаётся только на happy-path фидов

На 4xx-ответах `/feed/*` стоит `Content-Type: text/plain; charset=UTF-8` без `Cache-Control` — совпадает с кодом и ожиданиями.

## Воспроизведение

```bash
# в одном терминале
go run ./cmd/server

# в другом
bash tests/manual/run.sh
# → лог: tests/manual/run.log
```

Переменные окружения скрипта:

- `HOST` — базовый URL (дефолт `http://localhost:8000`)
- `LOG` — путь к лог-файлу (дефолт `tests/manual/run.log`)
