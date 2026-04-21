# Notifier manual test report — tg-channel-to-rss

- **Дата:** 2026-04-21 20:37–20:40 UTC
- **Ревизия:** `master` @ Goalt/llm-signals
- **Артефакты:**
  - e2e-драйвер: [tests/manual/notifier_e2e/main.go](notifier_e2e/main.go), лог: [tests/manual/notifier.log](notifier.log)
  - wiring-скрипт: [tests/manual/notifier-wiring.sh](notifier-wiring.sh), лог: [tests/manual/notifier-wiring.log](notifier-wiring.log)
- **Под тестом:**
  - [internal/notifier/notifier.go](../../internal/notifier/notifier.go) — полная семантика `Run`, seed-pass, dispatch, dedup, error handling.
  - [cmd/server/main.go](../../cmd/server/main.go) — функции `startNotifier` / `startXNotifier` и парсинг env (`TG_CHANNELS`, `WEBHOOKS`, `POLL_INTERVAL`, `X_USERS`, `X_BEARER_TOKEN`, `X_POLL_INTERVAL`).

## Сводка: e2e поведение (12 ассершенов)

Драйвер запускает нотификатор в процессе и поднимает реальные `httptest.Server` в роли вебхуков.

| # | Сценарий | Проверка | Вердикт |
|---|---|---|---|
| 1 | Seed: канал c 1 старым постом | `fetcher.calls["chanA"] >= 1` | ✅ |
| 2 | Seed pass не отправляет вебхуки | `hookA = hookB = 0` | ✅ |
| 3 | Добавлены 2 новых поста → 2 хука | `hookA = 2, hookB = 2` (4 HTTP POST за ~2 мс) | ✅ |
| 4 | Idle tick без изменений | deliveries не растут (`before==after`, оба =4) | ✅ |
| 5 | Payload: `{channel, item}` | `Channel=="chanA"`, `Item.ID ∈ {new-2,new-3}`, `Content-Type: application/json; charset=UTF-8` | ✅ |
| 6 | Хук отвечает 500 | доставка всё равно попытается, ошибка залогирована `notifier: webhook "…" failed: unexpected status 500` | ✅ |
| 7 | Фетчер падает на канале `bad` | ошибка не фатальна, канал продолжает опрашиваться (`calls["bad"]=3`) | ✅ |
| 8 | Параллельный канал `ok` не страдает | `calls["ok"]=3`, новая запись доставлена на `hookC` | ✅ |
| 9 | `Run` без `Channels` | `err == "notifier: no channels configured"` | ✅ |
| 10 | `Run` без `Webhooks` | `err == "notifier: no webhooks configured"` | ✅ |
| 11 | `Run` с `Interval <= 0` | `err == "notifier: interval must be positive"` | ✅ |
| 12 | Остановка по `ctx.Cancel` | `Run` возвращает `context.Canceled`, горутина завершается | ✅ |

**Итог:** 12/12 ✅, ALL ASSERTIONS PASSED (см. [notifier.log](notifier.log)).

Выжимка из лога:

```
== Scenario 1-3: seed, new-item dispatch, idle tick
PASS: seed pass polled the channel at least once (calls=2)
PASS: seed pass did NOT dispatch to hookA (got 0)
PASS: seed pass did NOT dispatch to hookB (got 0)
[hookA] ← POST /  ct="application/json; charset=UTF-8"  #1 item=new-2
[hookB] ← POST /  ct="application/json; charset=UTF-8"  #1 item=new-2
[hookA] ← POST /  ct="application/json; charset=UTF-8"  #2 item=new-3
[hookB] ← POST /  ct="application/json; charset=UTF-8"  #2 item=new-3
PASS: received 2 deliveries on each webhook (hookA=2 hookB=2)
PASS: idle ticks triggered no extra deliveries (before=4 after=4)

== Scenario 4: 500 receiver + fetch error on another channel
notifier: fetch "bad" failed: simulated upstream failure   (×3)
[hookC-500] ← POST /  ct="application/json; charset=UTF-8"  #1 item=o-2
notifier: webhook "http://127.0.0.1:36889" failed: unexpected status 500
PASS: failing webhook was still attempted (hookC=1)
PASS: bad channel kept being polled despite errors (calls=3)
PASS: ok channel kept being polled (calls=3)
```

## Сводка: wiring `cmd/server` (7 сценариев)

Скрипт поднимает бинарник `/tmp/tg-server` с разной комбинацией env, читает stdout 1.5 с и завершает SIGKILL. Проверяются startup-логи.

| # | Сценарий | Ожидание | Вердикт |
|---|---|---|---|
| 1 | Без env | оба нотификатора disabled, положительных `polling` нет | ✅ |
| 2 | `TG_CHANNELS=durov,telegram WEBHOOKS=a,b POLL_INTERVAL=10s` | `notifier: polling 2 channel(s) every 10s, dispatching to 2 webhook(s)` | ✅ |
| 3 | `X_USERS=jack WEBHOOKS=…` без `X_BEARER_TOKEN` | `x.com notifier disabled: set X_BEARER_TOKEN` | ✅ |
| 4 | `X_USERS=jack X_BEARER_TOKEN=fake WEBHOOKS=…` | `x.com notifier: polling 1 user(s) every 10s, dispatching to 1 webhook(s)` + реальный сетевой вызов к `api.x.com` → `401` в логе (ожидаемо для фейкового токена) | ✅ |
| 5 | TG + X одновременно | обе строки `polling` в stdout | ✅ |
| 6 | `POLL_INTERVAL=not-a-duration` | fatal: `invalid POLL_INTERVAL: time: invalid duration "not-a-duration"` | ✅ |
| 7 | `X_POLL_INTERVAL=bogus` | fatal: `invalid X_POLL_INTERVAL: time: invalid duration "bogus"` | ✅ |

**Итог:** 7/7 ✅, см. [notifier-wiring.log](notifier-wiring.log).

Пример лога для кейса 4 (подтверждение что сид-проход действительно бьёт в реальный upstream с тем токеном, что передали):

```
env: X_USERS=jack X_BEARER_TOKEN=fake-token WEBHOOKS=http://127.0.0.1:9/a X_POLL_INTERVAL=10s
notifier disabled: set TG_CHANNELS and WEBHOOKS to enable
x.com notifier: polling 1 user(s) every 10s, dispatching to 1 webhook(s)
Serving tg-channel-to-rss on http://127.0.0.1:18104
notifier: fetch "jack" failed: x.com API request failed with status 401
```

## Общий итог

**19/19 проверок ✅**

## Замечания и улучшения (найдены в ходе тестирования)

### ⚠ Shared `WEBHOOKS` — нет способа разделить вебхуки для TG и x.com

`TG_CHANNELS` и `X_USERS` используют **один и тот же** список `WEBHOOKS` (см. [cmd/server/main.go](../../cmd/server/main.go#L133)). Это значит, что оба нотификатора будут доставлять свои payload-ы на одни и те же URL-ы. В payload есть поле `channel`, которое содержит имя TG-канала или x.com-юзернейма — различить источники по содержимому можно, но настроить раздельные webhook-эндпоинты нельзя.

Предложение: `TG_WEBHOOKS` / `X_WEBHOOKS` с fallback на общий `WEBHOOKS`.

### ⚠ Seed-проход x.com нотификатора шумит в лог

При запуске с валидным форматом `X_BEARER_TOKEN`, но невалидным значением, seed-проход немедленно бьёт в `api.x.com` и логирует ошибку `fetch "jack" failed: x.com API request failed with status 401`. Это корректное поведение (тест это подтверждает), но нет graceful message «проверьте токен»: оператор увидит только 401 в логах.

Предложение: в [internal/xapi/service.go](../../internal/xapi/service.go#L143) дифференцировать `401 Unauthorized` → более явное сообщение (`invalid x.com bearer token`).

### ℹ Bot не стартует при ошибке форматирования `POLL_INTERVAL`

Как и ожидается: `log.Fatalf` ([main.go L112](../../cmd/server/main.go#L112) и [L143](../../cmd/server/main.go#L143)). Поведение задокументировано в README — тесты 6-7 подтвердили.

### ℹ `Content-Type` вебхука

Во всех 5 доставках из e2e-теста Content-Type был строго `application/json; charset=UTF-8` — совпадает с [internal/notifier/notifier.go](../../internal/notifier/notifier.go#L170). 

## Воспроизведение

```bash
# e2e прогон нотификатора
go build -o /tmp/notifier-e2e ./tests/manual/notifier_e2e
/tmp/notifier-e2e | tee tests/manual/notifier.log

# wiring-проверки startup-логов сервера
go build -o /tmp/tg-server ./cmd/server
bash tests/manual/notifier-wiring.sh
```

Переменные окружения:

- `BIN` — путь к собранному бинарю (дефолт `/tmp/tg-server`).
- `LOG` — путь к итоговому лог-файлу (дефолт `tests/manual/notifier-wiring.log`).
