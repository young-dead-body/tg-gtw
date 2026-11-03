# Архитектура «Защищённого шлюза доступа к Telegram Web»

> Обновление S19: DC WebSocket теперь идёт через основной origin
> `/tg/_gw/dc/<id>/apiws`, с проверкой сессии/версии пароля и отзывом
> соединений. Старые DC-поддомены и raw-TCP listener из исторических схем
> ниже выведены из эксплуатации. Бот использует постоянный Telegram ID и
> одноразовые ссылки вместо паролей. Актуальные контракты и порядок
> обновления — [HARDENING-S19.md](HARDENING-S19.md).


Подробный технический разбор того, как устроен и работает программный продукт.
Документ ориентирован на разработчиков и DevOps: описывает модули, потоки данных
и ключевые механизмы с кусками кода и схемами.

Связанные документы:
- [`README.md`](../README.md) — быстрый старт и эксплуатация.
- [`docs/152-fz/`](152-fz/) — комплект документов по 152-ФЗ (согласия, политики, журналы).

---

## 1. Назначение продукта

Корпоративный **веб-шлюз** к Telegram Web. Он решает две задачи:

1. **Контроль доступа.** Доступ к Telegram Web (клиенты A `/a`, K `/k`, Z `/z`)
   открыт только после:
   - авторизации во внутренней системе компании (email + пароль, сессия в Redis);
   - принятия обязательного согласия на обработку ПДн (152-ФЗ).
   Без активной сессии **и** принятого согласия доступ к `/tg/*` закрыт.

2. **Обход блокировок.** В РФ хосты `*.web.telegram.org` заблокированы, а
   корпоративные сети часто пропускают только порты 80/443. Шлюз забирает на
   себя **весь** трафик Telegram Web: и HTTP-страницы приложения, и MTProto-
   соединения к датацентрам (DC), и уводит их в интернет через SOCKS5, который
   не блокируется.

Проблема, которую решает система: браузер загружает приложение с нашего origin
(`/tg/*`), но Telegram Web сам ходит к своим серверам напрямую — к фиксированным
DC-хостам. Без перехвата этих соединений шлюз бесполезен. Поэтому в ядре системы
находятся два механизма: **TLS-passthrough** (для прямых, «зашитых» в клиент
соединений) и **DC-мост** с **перезаписью конфигурации** (для соединений, адреса
которых Telegram отдаёт динамически через `getConfig`).

---

## 2. Общая архитектура

Продукт — набор Docker-контейнеров (см. [`docker-compose.yml`](../docker-compose.yml)).
Внутренние порты пробрасываются наружу через `nginx`, который является единой
точкой входа.

```
                    ┌────────────────────────── ГЛЮЗ (host) ──────────────────────────┐
                    │                                                                  │
 Клиент (браузер)   │   ┌────────┐    ┌─────────┐   ┌──────────────┐                    │
   │ :443 HTTPS      │   │ nginx  │──► │ web     │   │ postgres:17  │                    │
   │                 │   │        │    │ (React) │   │ (таблицы)    │                    │
   │ / , /api/*      │   │ :80/443│    └─────────┘   └──────▲───────┘                    │
   │                 │   │ /tg/*  │─────────────────────► ┌─┴──────────┐  миграции      │
   │ /tg/*           │   └───┬────┘                       │    api     │  cron/бэкапы    │
   │                 │       │  /api/*                    │ (Fiber :8080)               │
   │                 │       └───────────────────────────►│ sessions/RL/audit           │
   │                 │                                    └─────┬───────┘               │
   │ MTProto DC s     │   ┌────────── telegram-proxy ───────┐    │                      │
   │ (passthrough)    │   │ HTTP-proxy :8080  (гейт+пул)    │    │                      │
   │  :9443 SNI       │   │ TLS-passthrough                 │    │                      │
   │                  │   │ DC-мост :9451..9455             │    └────► redis:6379       │
   │ MTProto (DC)     │   └──────────────┬──────────────────┘         (сессии/RL/events)│
   │  :20001..20005   │                  │                                  ▲            │
   │                  │                  ▼ SOCKS5 (ед. точка выхода)       │ pub/sub     │
   │                  │            ┌───────────┐        ┌────────┐         │            │
   │                  │            │ интернет  │◄──────►│  bot   │─────────┘            │
   │                  │            │ SOCKS5    │        └────────┘  (Telegram-бот)      │
   │                  │            └───────────┘               │                        │
   │                  │                                        └─► Telegram (уведомл.)  │
   └──────────────────┘                                          │                       │
                                                                 │
   Мониторинг: prometheus :9090 · grafana :3001 · loki :3100 · promtail (логи)
```

Ключевое: **`telegram-proxy` — единственная точка выхода в интернет** к Telegram.
Все его исходящие соединения (HTTP-прокси, passthrough, DC-мост, health-checks)
идут через единый `DialContext` (SOCKS5, [`dial.go`](../api/internal/proxy/dial.go))
— шлюзу самому не нужен маршрут/DNS к `telegram.org`.

### Модули

| Модуль | Куда | Что делает |
|---|---|---|
| `nginx` | `infra/nginx/` | Единая точка входа: SPA, `/api/*`, `/tg/*`, DC-bridge (stream). |
| `web` | `web/` | React 19 SPA: landing, логин, регистрация, согласия, админ-панель. |
| `api` | `api/` | Go/Fiber бэкенд: сессии, пользователи, согласия, аудит, статистика, cron, бот-API. |
| `telegram-proxy` | `api/internal/proxy` + `api/cmd/proxy` | Reverse-proxy Telegram Web + passthrough + DC-мост + перезапись конфига. |
| `bot` | `bot/` | Telegram-бот уведомлений (события из Redis pub/sub). |
| `postgres` | — | Персистентность: пользователи, аудит, статистика, согласия, пул бэкендов. |
| `redis` | — | Живые сессии, rate-limit, pub/sub событий, хранилище чатов бота. |
| `prometheus/grafana` | `infra/` | Метрики API и прокси (+ dashboard). |
| `loki/promtail` | `infra/` | Централизованные логи. |

---

## 3. Слой API (`api/`)

### 3.1 Запуск и порядок инициализации

Точка входа — [`api/cmd/api/main.go`](../api/cmd/api/main.go). Порядок старта:

1. Загрузка конфига (`PORT`, `POSTGRES_DSN`, `REDIS_ADDR`).
2. Структурный логгер (JSON zap).
3. Опциональные миграции (`RUN_MIGRATIONS`).
4. Пул подключений к Postgres.
5. **Bootstrap первого админа** (см. ниже).
6. Подключение к Redis.
7. Нотификатор событий (Redis pub/sub) и feature-toggle `telegram.bot`.
8. Geo-определение (опц., MaxMind `.mmdb`), rate-limiter'ы, lockout.
9. Prometheus-метрики на `:9091`, gauges зависимости (postgres/redis/proxy).
10. Регистрация роутов Fiber и запуск cron-задач.

```go
app := app.New(&app.Server{
    Queries:      db.New(pool),
    Sessions:     session.NewRedisStore(rdb),
    Hasher:       password.NewHasher(),
    Notifier:     notifier,
    Geo:          geoResolver,
    RateAPI:      limiter("rl:api:", ...),
    RateLogin:    limiter("rl:login:", ...),
    RateReg:      limiter("rl:registration:", ...),
    Lockout:      lockout,
    Metrics:      metrics,
    Logger:       logger,
    Config: &app.Config{
        SessionTTL:           24 * time.Hour,
        PasswordReuseHistory: 5,
        BotToken:             os.Getenv("BOT_TOKEN"),
        BotEnabled:           tgBotEnabled,
        GatewayURL:           getenv("GATEWAY_URL", "https://localhost"),
    },
}, app.HealthCheckers{ ... })
```

**Bootstrap первого админа** ([`seed.go`](../api/cmd/api/seed.go)) разрывает
«замкнутый круг»: admin-only эндпоинты (`POST /users`, одобрение заявок) требуют
уже существующего админа. API при старте создаёт первого админа, если в
таблице `users` ещё нет ни одного админа. Логин и пароль генерируются случайно
и выводятся в stdout API после записи в БД. Уже созданный админ **никогда не
перезаписывается**.

### 3.2 Роуты (регистрируются в [`app.go`](../api/internal/app/app.go))

| Метод и путь | Обработчик | Доступ | Назначение |
|---|---|---|---|
| `GET /swagger.json`, `/swagger` | inline | публичный | OpenAPI-спецификация + UI |
| `GET /health` | inline | публичный | liveness (postgres, redis) |
| `GET /features` | `handleFeatures` | публичный | feature-флаги (`telegram_bot`) |
| `POST /login` | `handleLogin` | CSRF+RL | вход |
| `POST /logout` | `handleLogout` | auth+CSRF | выход |
| `GET /me` | `handleMe` | auth | текущий пользователь |
| `POST /me/password` | `handleChangePassword` | auth+CSRF | смена пароля |
| `POST /registration` | `handleRegistration` | CSRF+RL | подача заявки |
| `GET /consent` | `handleConsentInfo` | публичный | метаданные согласия |
| `POST /consent` | `handleConsent` | CSRF | принятие согласия |
| `GET/POST/PATCH/DELETE /users/*` | users | admin | управление пользователями |
| `GET /stats`, `/logs`, `/dashboard` | stats/audit/admin | admin | аналитика и аудит |
| `GET/PATCH /registration-requests/*` | admin | admin | ревью заявок |
| `GET/PUT /settings/:key` | admin | admin | настройки (см. whitelist) |
| `POST /bot/credentials` | `handleBotCredentials` | bot-token | выдача пароля ботом |
| `GET /bot/users/:id` | `handleBotUser` | bot-token | получение пользователя ботом |

Глобальные middleware (порядок): `Metrics` → `requestLogger` → публичные роуты →
`ensureCSRFCookie` → `rateLimit(RateAPI)` → аутентифицированные роуты.

### 3.3 Поток входа ([`auth.go`](../api/internal/app/auth.go))

Это самый важный пользовательский сценарий:

```go
// handleLogin — сокращённо
func (s *Server) handleLogin(c *fiber.Ctx) error {
    // 1. парсинг {email, password}
    // 2. lockout: ключи "ip:"+c.IP() и "email:"+email проверяются через Blocked
    // 3. GetUserByEmail(email)
    // 4. Hasher.Verify (Argon2id)
    // 5. проверки статуса: IsActive, BlockedAt, ExpiresAt
    // 6. проверка ротации пароля: PasswordChangedAt + RotationPeriod <= now -> 403
    // 7. Sessions.Create(ctx, u.ID, ttl)          // Redis-сессия
    // 8. сброс lockout, recordLoginStatistics       // + аудит login.success
    // 9. linkConsent                                // привязка анонимного согласия
    //10. установка cookie tg_session и возврат {user}
}
```

Cookie сессии — серверная, в Redis, на клиенте только идентификатор:

```go
c.Cookie(&fiber.Cookie{
    Name:     "tg_session",
    Value:    sessionID,
    Path:     "/",
    Expires:  time.Now().Add(ttl),
    HTTPOnly: true,      // JS не видит
    Secure:   true,      // только по HTTPS
    SameSite: "Strict",  // защита от CSRF на уровне cookie
})
```

**Сессии** ([`session/store.go`](../api/internal/session/store.go)) — Redis:
- `session:<uuid>` → userID (TTL = время жизни сессии);
- `user_sessions:<userID>` → множество ID сессий (для массового ревока).

Время жизни сессии по умолчанию — **24 часа** (сверху через `SessionTTL`).

### 3.4 Регистрация и одобрение админом

**Регистрация** ([`registration.go`](../api/internal/app/registration.go)):
пользователь подаёт заявку (`first_name`, `last_name`, `email`, `telegram` и др.)
с валидацией; создаётся запись `registration_requests` со статусом `pending`;
нотифицируется админ; пишется аудит `registration.requested`.

**Одобрение** ([`admin.go`](../api/internal/app/admin.go)): админ ставит статус
`approved`/`rejected`. При подтверждении на основе заявки **создаётся пользователь**
(`CreateUser`, роль `user`); пишется аудит `registration.approved`. После этого
пользователь может войти и (при необходимости) получить пароль через бота.

### 3.5 Согласие ([`consent.go`](../api/internal/app/consent.go))

`POST /consent {accepted:true, document_version}` — требования 152-ФЗ. Согласие
привязывается к аутентифицированной сессии, если она есть, иначе — анонимно
(IP + User-Agent). При последующем входе `linkConsent` находит анонимное согласие
по `(ip, user_agent)` и привязывает к пользователю. Версия документа берётся из
настройки `consent.document_version` (по умолчанию `"1.0"`; при смене версии
пользователи должны принять заново).

Связывание анонимного согласия — best-effort (совпадение по IP/UA ненадёжно:
за nginx в `c.IP()` для всех один внутренний IP, UA должен совпасть байт-в-байт).
Поэтому `/login` и `/me` возвращают флаг `consent_required`: если у
идентифицированного пользователя нет записи в `consents`, SPA показывает баннер
повторно, и `POST /consent` уже с сессией фиксирует согласие **за аккаунтом**
(`userConsentRequired` в `auth.go`). Это гарантирует, что до `/tg/` у
авторизованного пользователя всегда есть запись согласия.

Именно это согласие проверяет и `gate` в telegram-proxy (см. §4.2).

### 3.6 Пароли и политика ротации

- Хэширование — **Argon2id** ([`password.go`](../api/internal/password/password.go)):

```go
const (
    version     = 19        // Argon2id v19
    memory      = 64 * 1024 // 64 MiB
    iterations  = 1
    parallelism = 1
    saltLength  = 16
    keyLength   = 32
)
// формат: $argon2id$v=19$m=65536,t=1,p=1$<salt>$<key>
```

- **Политика ротации** ([`policy.go`](../api/internal/policy/policy.go)): по
  умолчанию `RotationPeriod = 90 дней`, `WarningWindow = 14 дней`. Значения
  берутся из настроек `password.rotation_period_days` / `password.warning_days`.
- При смене пароля проверяется история (`password_history`) — нельзя повторять
  недавние пароли (по умолчанию последние 5). Обновляется `password_changed_at`.
- Истёкший пароль блокирует вход (`403 password_expired`); cron-задача
  (`cron/password.go`) заранее присылает уведомления `password.expiring`.

### 3.7 Защита от перебора ([`ratelimit`](../api/internal/ratelimit), [`security.go`](../api/internal/app/security.go))

- **Rate-limit** (Redis, фиксированное окно): `/login` 10/мин, `/registration`
  5/мин, API в целом — 120/мин. `INCR` + `EXPIRE` только при первом инкременте.
- **Lockout**: 5 неудачных попыток за 15 минут → блокировка по `ip:` и `email:`;
  снимается при успешном входе.
- **CSRF** — double-submit: cookie `tg_csrf` (32 случайных байта, hex) +
  заголовок `X-CSRF-Token` для `POST/PUT/PATCH/DELETE`, сравнение
  `subtle.ConstantTimeCompare`.

### 3.8 Статистика и аудит

- **Статистика** ([`statistics.go`](../api/internal/app/statistics.go)): на каждый
  вход (успешный и нет) пишется строка: IP, geo (страна/регион/город), язык,
  браузер/ОС/устройство (`ua.Parse`), разрешение экрана (из заголовка
  `X-Screen-Resolution`), `login_at`, `auth_error`. При `logout` заполняется
  `session_duration_seconds`.
- **Аудит** ([`audit.go`](../api/internal/app/audit.go)): неизменяемый журнал
  событий (`login.success`, `registration.approved`, `consent.accepted`,
  `credentials.issued` и др.) с actor, IP, UA и JSON-details. Просмотр — admin
  `GET /logs`.

### 3.9 Cron-задачи ([`cron/`](../api/internal/cron))

| Задача | Интервал | Действие |
|---|---|---|
| Password expiry | настраиваемый | уведомления `password.expiring` / `password.expired` |
| Access expiry | настраиваемый | уведомления `access.expiring` / `access.expired` (до `expires_at`) |
| Cleanup | 24h | удаление протухших сессий и старых `pending/rejected` заявок |
| Archive | 24h | перенос старых `audit_log` в `audit_log_archive` (атомарный CTE) |
| Backup | 24h | `pg_dump` в `/backups`, прунинг старше `BACKUP_KEEP` |

### 3.10 Feature-toggle `telegram.bot`

Вся интеграция с Telegram-ботом (контейнер `bot`, эндпоинты `/bot/*`, публикация
событий, поле `telegram` в UI) управляется флагом `telegram.bot`:

- Приоритет значения: **строка в таблице `settings` > env `TG_BOT_ENABLED` >
  встроенный дефолт `true`**.
- При старте API значение из env сеется в таблицу (`feature.Seed`) один раз.
- Далее меняется на лету через `PUT /settings/telegram.bot` без перезапуска.
- Есть публичный `GET /features` → `{telegram_bot}` для фронтенда.

---

## 4. Модуль telegram-proxy ([`api/internal/proxy`](../api/internal/proxy))

Это ядро системы. Точка входа — [`api/cmd/proxy/main.go`](../api/cmd/proxy/main.go),
который поднимает **три** слушателя:

| Слушатель | Env | Назначение |
|---|---|---|
| HTTP reverse-proxy `:8080` | `PROXY_ADDR` | `/tg/*`, гейт + пул бэкендов |
| TLS-passthrough `:9443` | `TG_PASSTHROUGH_ADDR` | прямые MTProto-соединения `*.web.telegram.org:443` |
| DC-мост `:9451..:9455` | `TG_DCPROXY_BASE_PORT` | приём переписанных DC-соединений, повторный TLS к Telegram |

```
Браузер ─► :8080  (http-proxy: гейт → пул → reverse-proxy → rewrite)
Браузер ─► :9443  (passthrough: SNI-сниф *.web.telegram.org → SOCKS5 → DC)
Nginx   ─► :9451..9455  (DC-мост: ---input plaintext--- → SOCKS5+TLS → DC)
```

### 4.1 Поток HTTP-запроса `/tg/*` ([`proxy.go`](../api/internal/proxy/proxy.go))

```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if !h.gate(w, r) {
        return
    }
    h.proxy(w, r)
}
```

**Гейт** (сессия + согласие):

```go
func (h *Handler) gate(w http.ResponseWriter, r *http.Request) bool {
    if isAnonymousAsset(r.URL.Path) { return true } // manifest/sw/favicon без сессии

    sid := cookieValue(r)
    if sid == "" { http.Error(w, "unauthorized", 401); return false }

    userID, err := h.Sessions.Get(ctx, sid)          // Redis
    if err != nil { ..., return false }

    u, err := h.Queries.GetUserByID(ctx, id)         // Postgres
    if !u.IsActive || u.BlockedAt.Valid { 403 }
    if u.ExpiresAt.Valid && past { 403 }

    ok, err := h.Queries.HasUserConsent(ctx, id)     // обязательное согласие
    if err != nil || !ok { 403 "consent required" }
    return true
}
```

Анонимные статические ассеты (`manifest.json`, `sw*.js`, `favicon*`) пропускаются
без сессии, иначе Service Worker и прочее сломались бы 401-ми.

**Пул бэкендов и фейловер** — [`pool.go`](../api/internal/proxy/pool.go):

```go
// health-check = TCP-коннект через тот же Dial (SOCKS5)
func (p *Pool) Healthy(ctx context.Context, b db.ProxyPool) bool {
    dial := p.Dial                     // nil -> обычный Dialer
    conn, err := dial(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
    ...
}
// StartHealthChecks — раз в 30с обходит ВСЕ бэкенды из proxy_pool,
// помечает активные/неактивные и "оживляет" неактивные, если снова доступны.
```

`proxy()` берёт активные бэкенды из `proxy_pool` (порядок — priority) и пытается
прокинуть запрос через каждый; если бэкенд недоступен — помечает `inactive` и
переключается на следующий. Все недоступны → `502`.

**`forward`** использует `httputil.ReverseProxy`:

```go
Director: func(req *http.Request) {
    h.rewriteTarget(req)                 // убрать префикс /tg
    req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
    if base := strings.TrimSuffix(target.Path, "/"); base != "" {
        req.URL.Path = base + req.URL.Path  // подставить базовый путь бэкенда (/a, /k, /z)
    }
    req.Header.Set("X-Forwarded-Proto", "https")
    ...
},
Transport:     h.transport,   // retryTransport (GET/HEAD)
FlushInterval: -1,            // стриминг медиа/загрузок сразу
ModifyResponse: func(resp) {
    // 1. text/html -> Cache-Control: no-cache (иначе браузер держит устаревшую страницу)
    // 2. если это TL config -> внутри h.dcRewrite() перезаписать dc_options
    // 3. если это worker bundle -> перезаписать bootstrap WS (bundle_rewrite.go)
},
```

**retryTransport** ([`proxy.go:341`](../api/internal/proxy/proxy.go)): повторяет
идемпотентные GET/HEAD при ошибке соединения или временных 502/503 (это частый
кейс при выходе через SOCKS5). POST и прервавшиеся в середине потоки не
повторяются — их нельзя безопасно ретраить.

### 4.2 TLS-passthrough ([`passthrough.go`](../api/internal/proxy/passthrough.go))

Telegram Web внутри браузера ходит к DC напрямую по `https://*.web.telegram.org:443`
(хосты `venus`, `aurora`, `pluto`, `vesta`, `flora` + `web.telegram.org`).
Слушатель `:9443` (хостовый порт 443 → passthrough) перехватывает эти соединения:

```
Браузер TLS (MTProto) ──► :9443 Passthrough.Serve
   │  handle(conn)
   │    ├─ peekSNI(br)          ← читаем ClientHello, достаём server_name
   │    ├─ isTelegramWebHost?   ← нет → закрываем
   │    ├─ Dial via SOCKS5 ──► host:443
   │    └─ splice(conn, up, MultiReader(peekedBytes, остаток))
   │          ↑ переигрываем peeked ClientHello, затем io.Copy в обе стороны
   ▼
Реальный DC (venus.web.telegram.org:443, ...)
```

Важная особенность: passthrough **не терминирует TLS**, сертификаты шлюза не
участвуют — TLS остаётся end-to-end «браузер ↔ Telegram». Он лишь смотрит SNI
из ClientHello и решает, куда увести raw-TCP-поток.

```go
func (p *Passthrough) handle(conn net.Conn) {
    br := bufio.NewReaderSize(conn, 32*1024)
    raw, host, err := peekSNI(br)          // читаем много-записевый ClientHello
    if err != nil || !isTelegramWebHost(host) {
        conn.Close(); return
    }
    up, err := p.Dial(ctx, "tcp", net.JoinHostPort(host, "443"))  // через SOCKS5
    ...
    p.splice(conn, up, io.MultiReader(bytes.NewReader(raw), br))
}
```

### 4.3 Перезапись конфигурации (TL) — [`tl.go`](../api/internal/proxy/tl.go)

Это и есть «точка перехвата» для динамически отдаваемых DC. Когда Telegram Web
вызывает `getConfig`, в ответе приходит TL-структура `config#330b4067` с вектором
`dcOption#5d8c6cc`. Прокси парсит это бинарное тело (little-endian) и переписывает
адреса DC на шлюз:

```go
// Два режима:
//  Domain:  каждый DC -> dc<N>.<Domain>:443   (SNI-роутинг, приоритет)
//  Legacy:  каждый DC -> IP:PortBase+dc_id    (LAN)
func (rw RewriteConfig) host(o dcOption) string { ... }
func (rw RewriteConfig) port(o dcOption) int    { ... }
```

```go
func rewriteConfigTL(body []byte) ([]byte, bool, error) {
    // дискриминатор: это config#330b4067?
    // (учитываем 4-байтовый префикс content_length — MTProto HTTP transport)
    // парсим flags, this_dc, count, затем каждый dcOption
    //   - secret сохраняется байт-в-байт (opaque slice)
    //   - CDN/media-only/IPv6 узлы НЕ трогаем (не туннелируются)
    //   - остальные переписываем на rw.host/id + rw.port/id
    // пересобираем тело, обновляем content-length префикс
}
```

`isTLPayload` — эвристика «это может быть config»: HTTP 200, нет
`Content-Encoding` (gzip спрятал бы конструктор), бинарный content-type, тело
≤ 1 MiB (конфиг — это несколько КБ). Всё, что не похоже на config, проходит
без изменений.

```
Browser ──getConfig──► proxy
                          │  ModifyResponse: тело = TL config?
                          │    └─ rewriteConfigTL: dc_options[] -> DC-мост
                          ▼
Telegram Web получает dc_options, указывающие НА ШЛЮЗ
                          ▼
Browser коннектится к DC-мосту (nginx → telegram-proxy:945X)
```

### 4.4 Перезапись bootstrap-бандла — [`bundle_rewrite.go`](../api/internal/proxy/bundle_rewrite.go)

Проблема «курицы и яйца»: **до первого `getConfig`** клиент использует зашитые в
worker-бандл bootstrap-эндпоинты `wss://kws<N>.web.telegram.org/apiws`. Рерайт
`dc_options` не успевает сработать для первого соединения. Поэтому прокси
переписывает сам JS-бандл (`index.worker-<hash>.js`):

```go
var wsBootstrapRE = regexp.MustCompile(
    `wss://\$\{[A-Za-z_$][A-Za-z0-9_$]*\.suffix\.toLowerCase\(\)\}` +
    `ws\$\{([A-Za-z_$][A-Za-z0-9_$]*)\}\$\{[A-Za-z_$][A-Za-z0-9_$]*\}\.web\.telegram\.org` +
    `/\$\{([A-Za-z_$][A-Za-z0-9_$]*)\}`,
)
```

Регулярка устроена структурно: закреплены только литералы и имя свойства
`.suffix`, а имена минифицированных переменных (dc, path) захватываются группами.
Это переживает переименования минификатора в новых версиях Telegram.

- **Domain-режим** → `wss://dc${dc}.<Domain>/${path}` (порт 443).
- **Legacy** → `wss://<IP>:${<portBase>+dc}/${path}` (порт — runtime-шаблон).

**Промах рерайта fail-safe**: тело отдаётся без изменений, пишется громкий
warning в лог и метрика `telegram_gateway_proxy_rewrite_total{kind="bundle",result="miss"}`
— это канареечный индикатор того, что Telegram поменял форму бандла и QR-вход
сломается.

### 4.5 DC-мост ([`dcproxy.go`](../api/internal/proxy/dcproxy.go))

Принимает соединения от nginx **после** того, как переписанные `dc_options`
привели браузер на шлюз. Карта DC→хост:

```go
var DCHosts = map[int]string{
    1: "venus.web.telegram.org",
    2: "aurora.web.telegram.org",
    3: "vesta.web.telegram.org",
    4: "pluto.web.telegram.org",
    5: "flora.web.telegram.org",
}
```

Архитектура **двойного TLS**:

```
Browser MTProto ──TLS──► nginx :2000X (терминирует TLS самоподписанным сортом)
                              │  plaintext (nginx stream)
                              ▼
   telegram-proxy :945X (DCProxy)
     │  Dial через SOCKS5 ──► <real DC>:443
     │  tls.Client(up, ServerName=host)   ← второй TLS, проверка оф. сертификата
     └─ splice(client, tlsConn, client)   ← duplex
```

То есть: браузер→шлюз (первый TLS, самоподписанный/LE), шлюз→Telegram (второй
TLS, официальный сертификат). MTProto проходит сквозь оба туннеля.

### 4.6 Метрики ([`metrics.go`](../api/internal/proxy/metrics.go))

На `:9092`, namespace `telegram_gateway_proxy_*`:
- `backend_active{url}` — gauge доступности каждого бэкенда;
- `requests_total{method}`, `request_duration_seconds` — нагрузка;
- `rewrite_total{kind,result}` — перезапись config/bundle, `hit`/`miss`
  (канареечная метрика).

---

## 5. Веб-интерфейс ([`web/`](../web))

React **19** + TS + Vite 6 + Tailwind v3 (про **shadcn/ui**: конфиг
`components.json` есть, но реальные компоненты написаны вручную на Tailwind).
Роутера нет — переключение видов это локальный state-machine в
[`App.tsx`](../web/src/App.tsx).

### 5.1 Структура видов

```
<FeaturesProvider>                     // GET /api/features, кэш + дефолты
  <AppContent>
    boot(): GET /api/me
      ├─ роль admin     -> AdminPanel (6 вкладок)
      ├─ авторизован
      │    └─ consent_required=false -> redirectToTg() — /tg/
      │    └─ consent_required=true  -> Landing + ConsentBanner (повторное
      │                                 согласие за аккаунт), после принятия
      │                                 POST /api/consent + redirectToTg()
      └─ нет           -> GET /api/consent -> Landing + ConsentBanner
    view === "login"    -> LoginForm
    view === "register" -> RegisterForm
    default             -> Landing (+ ConsentBanner)
```

Вкладки админ-панели (`AdminPanel.tsx`): Dashboard, Users, Requests, Stats,
Logs, Settings.

### 5.2 API-клиент и CSRF ([`src/lib/api.ts`](../web/src/lib/api.ts))

```ts
const API_BASE = "/api";
const CSRF_COOKIE = "tg_csrf";

async function request<T>(path, init) {
  const res = await fetch(`${API_BASE}${path}`, { credentials: "include", ...init });
  if (!res.ok) throw new ApiClientError(res.status, message, fieldErrors);
  return res.json();
}
// api.post/put/patch/del добавляют csrfHeaders(): X-CSRF-Token из cookie tg_csrf
```

- Base URL — same-origin `/api`.
- `credentials: "include"` — сессионная cookie.
- **CSRF double-submit**: cookie `tg_csrf` эхо-отражается в `X-CSRF-Token` для
  всех state-changing методов.
- В dev Vite проксирует `/api` → `localhost:8080` со `rewrite: /^\/api/`.

### 5.3 Потоки пользователя

**Consent → Login → /tg/**:
```
Landing → (кнопка "Войти" видна только после принятия согласия)
  ConsentBanner: POST /api/consent {accepted, document_version}
                 + localStorage "tg_consent_accepted"
  LoginForm: POST /api/login {email, password} + заголовок X-Screen-Resolution
             -> response consent_required=false -> redirectToTg() -> /tg/
             -> response consent_required=true  -> ConsentBanner (уже с
                сессией; POST /api/consent фиксирует согласие за аккаунт)
                -> redirectToTg() -> /tg/
```

**Регистрация → одобрение админом**:
```
RegisterForm: POST /api/registration
              -> "Заявка отправлена"; fieldErrors рендерятся инлайн
  Admin: RequestsTab -> PATCH /api/registration-requests/{id} {status}
                       -> новый пользователь создан, можно входить
```

**Feature-toggle в UI**: поле `Telegram` (в `RegisterForm`, `UsersTab`,
`RequestsTab`, CSV-экспорте) рендерится только если
`useFeatures().telegram_bot === true`.

### 5.4 Прочее

- [`src/lib/export.ts`](../web/src/lib/export.ts) — CSV (RFC-4180, `;`,
  UTF-8 BOM для Excel) и печать в PDF.
- [`src/lib/consent.ts`](../web/src/lib/consent.ts) — localStorage-хранение
  принятой версии согласия.
- Тесты: Vitest + Testing Library + jsdom.

---

## 6. Telegram-бот ([`bot/`](../bot))

Бот уведомлений (расшифровка `bot/` ниже) потребляет события из Redis
`gateway:events` и отправляет пользователям Telegram уведомления (истечение
доступа/пароля) и выдаёт учётные данные.

### 6.1 Старт ([`bot/cmd/bot/main.go`](../bot/cmd/bot/main.go))

- Если `TG_BOT_ENABLED=false` — чисто завершается (нет restart-петли).
- Обязательные env: `TELEGRAM_BOT_TOKEN`, `API_TOKEN` (общий сервисный токен).
- Два параллельных goroutine: Redis pub/sub-потребитель и poller telebot.
- Graceful shutdown по SIGINT/SIGTERM.

### 6.2 Сервис ([`bot/internal/app/service.go`])

Команды:
- `/start` — сохраняет привязку `username → chatID`, приветствие.
- `/credentials` — вызывает API `POST /bot/credentials` → получает
  `{email, password, gateway_url}` и присылает их пользователю. Маппинг ошибок
  API в русские сообщения (404 → «аккаунт не найден», 403 → «заблокирован» и т.д.).
- `/help` — список команд.

Обработка событий (`HandleEvent`): принимает `access.expiring/expired`,
`password.expiring/expired/changed`. Для `password.changed` — дедупликация, если
пароль выдал сам бот в течение минуты. Затем ищет chatID по username в Redis и
шлёт уведомление.

### 6.3 Контракт с API

- API публикует события как `{event, user_id, at}` в `gateway:events`
  ([`notify/notifier.go`](../api/internal/notify/notifier.go)).
- Бот аутентифицируется **Bearer-токеном** (`API_TOKEN`), общим с `BOT_TOKEN` на
  API (server-side — `subtle.ConstantTimeCompare`).
- Хранилище чатов — Redis: ключ `bot:chat:<username>`, TTL 365 дней
  ([`store/chat.go`](../bot/internal/store/chat.go)).

---

## 7. Инфраструктура ([`infra/`](../infra))

### 7.1 Nginx — веб-вход и DC-bridge

[`nginx.conf`](../infra/nginx/nginx.conf) содержит **два блока**:

**HTTP-блок** — точка входа веб-интерфейса:
- `location /` → SPA (`web:80`);
- `location /api/` → API (`proxy_pass http://${API_UPSTREAM}/;` — trailing slash
  срезает префикс `/api`);
- `location /tg/` → telegram-proxy (**с отложенным upstream через переменную** +
  Docker DNS resolver `127.0.0.11`, чтобы nginx переживал пересоздание контейнера
  telegram-proxy);
- в `/tg/` принудительно `proxy_set_header Accept-Encoding "";` — иначе nginx не
  смог бы переписывать сжатые `Content-Encoding`'d тела (worker-bundle, config);
- nested-locations `^/tg/(assets|fonts)/` — кэш статики (`tg_static`);
- `^/tg/(sw-*.js|*.webmanifest)$` — кэш сервис-воркера с ревалидацией.

**Stream-блок** — DC-bridge на TCP-уровне:

| Порт | Назначение | Бэкенд в telegram-proxy |
|---|---|---|
| `20001..20005` (ssl) | браузер MTProto (самоподписанный серт, LAN) | `9451..9455` |
| `21001..21005` (plaintext) | локальная сторона frp-туннеля (VPS TLS) | `9451..9455` |

### 7.2 Шаблонизация через envsubst

[`templates/gateway.conf.template`](../infra/nginx/templates/gateway.conf.template)
— полный production-вход, обрабатывается на старте контейнера nginx.

```yaml
# docker-compose.yml — подставляются ТОЛЬКО перечисленные переменные,
# чтобы runtime-переменные nginx ($host, $request_uri и т.д.) не затирались.
NGINX_ENVSUBST_FILTER: API_UPSTREAM|WEB_UPSTREAM|TG_UPSTREAM|TLS_CERT|TLS_KEY|DC_BRIDGE_WSS_[1-5]
```

**CSP** в `/tg/` разрешает cross-origin соединения Telegram Web: `connect-src`
содержит `https://*.web.telegram.org`, `wss://*.web.telegram.org` **и** DC-bridge
origins `${DC_BRIDGE_WSS_1..5}` (куда приходят переписанные MTProto WebSockets).

### 7.3 Мониторинг и логи

- **Prometheus** scrapes `api:9091` и `telegram-proxy:9092`.
- **Grafana** provisioning: датасорсы Prometheus+Loki, дашборд
  `telegram-gateway.json` (6 панелей: RPS, latency p95, аптайм зависимостей,
  прокси-метрики, ошибки 5xx).
- **Loki** (retention 14 дней) + **Promtail** забирает `/logs/*.log` (общий volume
  `logs`, куда API пишет `LOG_FILE=/logs/app.log`).

### 7.4 Публичный вход

Актуальная схема с одним доменом и защищёнными DC WebSocket описана в [HARDENING-S19.md](HARDENING-S19.md). Развёртывание на VPS и через домашний сервер описано в [deploy/README.md](deploy/README.md).

---

## 8. Сквозной сценарий пользователя

```
1. Пользователь открывает https://<шлюз>/
      nginx / -> web (SPA)            [consent required, кнопки входа скрыты]
2. Принимает согласие
      POST /api/consent {accepted:true, version}  -> консент записан + аудит
3. Входит
      POST /api/login -> сессия в Redis, cookie tg_session -> redirect /tg/
4. Браузер идёт на /tg/*
      nginx -> telegram-proxy:8080 -> gate (сессия + согласие) -> пул -> backend
      Telegram Web (web.telegram.org/a или /k) отдаёт приложение + dc_options
      прокси переписывает dc_options/bootstrap на шлюз (tl.go, bundle_rewrite.go)
5. Telegram Web открывает MTProto-соединения
      wss://dc<N>.<domain>:443  (domain-режим)  или  wss://IP:2000X  (legacy)
      -> nginx stream -> DC-мост :945X -> SOCKS5+TLS -> реальный Telegram DC
      (прямые hardcoded *.web.telegram.org:443 идут через TLS-passthrough :9443)
6. Пользователь пользуется чатами; медиа/загрузки стримятся через proxy
```

---

## 9. База данных (миграции [`api/migrations`](../api/migrations))

| Таблица | Миграция | Назначение |
|---|---|---|
| `users` | 000001 | аккаунты (роль user/admin, is_active, blocked_at, expires_at) |
| `sessions` | 000002 | метаданные сессий (в отличие от живых сессий в Redis) |
| `registration_requests` | 000003 | заявки на доступ (pending/approved/rejected) |
| `audit_log` | 000004 | неизменяемый журнал аудита |
| `statistics` | 000005 | аналитика входов (IP, geo, браузер, длительность) |
| `consents` | 000006 | принятые согласия (152-ФЗ) |
| `password_history` | 000007 | окно запрета повторного использования пароля |
| `settings` | 000008 | key/value (consent version, password policy, feature toggles) |
| `proxy_pool` | 000009 | бэкенды telegram-proxy (url, status, priority) |
| `users.password_changed_at` | 000010 | метка ротации пароля |
| index `users(telegram)` | 000011 | поиск по telegram (lower, ci) для бота |
| `audit_log_archive` | 000012 | архив старых записей аудита |

SQL-запросы генерируются **sqlc** (в [`api/db/queries/*.sql`](../api/db/queries)),
результат — в [`api/internal/db/`](../api/internal/db). В CI есть проверка
`git diff` на расхождение сгенерированного кода.

---

## 10. Безопасность (сводно)

- **Сессии** — серверные, в Redis; на клиенте только HttpOnly+Secure+SameSite=Strict
  cookie `tg_session`. Токены не хранятся в LocalStorage.
- **CSRF** — double-submit (`tg_csrf` cookie + `X-CSRF-Token`).
- **Пароли** — Argon2id, политика ротации (90 дней), история (5), блокировка
  истёкших.
- **Перебор** — rate-limit (Redis) + lockout (5 попыток / 15 мин).
- **Доступ** — гейт в telegram-proxy: только активная сессия **и** принятое
  согласие; анонимные ассеты исключены.
- **Аудит** — неизменяемый журнал + архив; соответствие 152-ФЗ
  ([`docs/152-fz/`](152-fz/)).
- **Бот-API** — общий Bearer-токен, сравнение в константном времени; гейт через
  feature-toggle.
- **TLS** — TLS 1.3 + HSTS + строгий CSP; доверенный внутренний CA для
  trustworthy-origin Service Worker (иначе SW не регистрируется).

---

## 11. Глоссарий

| Термин | Значение |
|---|---|
| **DC** | датацентр Telegram (1..5); клиент держит MTProto-соединение с DC |
| **MTProto** | протокол обмена Telegram (в Web идёт поверх WebSocket/TLS) |
| **SNI** (Server Name Indication) | поле в TLS ClientHello — какой хост запрошен |
| **SOCKS5** | прокси-протокол; шлюз через него ходит в интернет к Telegram |
| **Passthrough** | TCP-туннель без терминирования TLS: только SNI-сниф и пересылка |
| **DC-мост** | серверный слой, принимает MTProto и повторно TLS-идет к реальному DC |
| **dc_options / getConfig** | TL-ответ, где Telegram отдаёт адреса своих DC |
| **bootstrap WS** | зашитые в worker-бандл `wss://kwsN.web.telegram.org` — первый коннект |
| **frp** | Fast Reverse Proxy — туннель из локальной сети наружу через VPS |
| **Feature-toggle** | флаг `telegram.bot`, включающий/выключающий интеграцию с ботом |
