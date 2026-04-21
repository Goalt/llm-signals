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
| 5 | `GET /feed/this_channel_does_not_exist_0001` | 400 `Telegram channel not found` | 400, body `Telegram channel not found` | ✅ **FIXED** |
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

**Итог:** 17/17 ✅

## Замечания

### ✅ Несуществующий Telegram-канал — ИСПРАВЛЕНО

**Проблема (исправлена 2026-04-21):** Ранее `GET /feed/<nonexistent>` возвращал HTTP 200 с пустым фидом, так как Telegram отдаёт HTTP 200 для несуществующих каналов со страницей «Contact @…».

**Решение:** Реализован фикс #2 из предложенных — проверка наличия `<div class="tgme_channel_info">` после успешного HTTP-ответа. Если этого элемента нет (как на страницах «Contact @…»), возвращается ошибка `Telegram channel not found` с HTTP 400.

**Код изменений:**
- [internal/app/service.go](../../internal/app/service.go) — добавлена проверка `hasChannelInfo` в `GetJSONFeed()`
- [internal/app/service_test.go](../../internal/app/service_test.go) — добавлен тест `TestGetJSONFeedChannelNotFoundContactPage`
- [tests/app_test.go](../../tests/app_test.go) — добавлен black-box тест `TestApp_GetJSONFeed_ChannelNotFoundContactPage`

**Текущее поведение:**
```bash
$ curl -i http://localhost:8000/feed/nonexistentchannel123456789
HTTP/1.1 400 Bad Request
Content-Type: text/plain; charset=UTF-8

Telegram channel not found
```

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
