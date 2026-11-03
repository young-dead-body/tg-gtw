# Защищённый шлюз доступа к Telegram Web

Корпоративный веб-шлюз: доступ к Telegram Web только после авторизации во внутренней системе компании и принятия обязательных согласий.

Это публичный снимок проекта без данных рабочего окружения и истории закрытого репозитория. Перед запуском задайте собственные секреты, домен и сетевые адреса. Настройки, характерные для исходного хоста, описаны в [docs/HOST-SPECIFIC.md](docs/HOST-SPECIFIC.md).

Исходный код опубликован для изучения. Лицензия на использование и распространение пока не указана; до её выбора действуют стандартные авторские права.

- Развёртывание в прод (VPS, домашний сервер, перенос между ними): [`docs/deploy/README.md`](docs/deploy/README.md)
- Смена пароля администратора: [`docs/gateway-admin.md`](docs/gateway-admin.md)

## Структура

```
├── api/       # Go + Fiber backend
├── web/       # React 19 + TS + Vite + Tailwind + shadcn/ui
├── bot/       # Telegram bot (S11)
├── infra/     # nginx, prometheus, grafana, loki
└── .github/   # GitHub Actions CI
```

## Стек

Go 1.25 / Fiber / pgx+sqlc · React 19 / TS / Vite / Tailwind / shadcn-ui · PostgreSQL 17 · Redis · Nginx · Docker Compose · Prometheus / Grafana / Loki · GitHub Actions.

## Развёртывание на VPS

Прод-шлюз на собственном VPS с доменом и сертификатом Let's Encrypt.
Достаточно VPS с 1 vCPU / 1 ГБ ОЗУ: образы собираются на вашей машине,
мониторинг по умолчанию выключен. Подробности по каждому шагу —
[`docs/deploy/VPS.md`](docs/deploy/VPS.md).

**Нужно:** VPS (Ubuntu 22.04+ / Debian 12+, root по ssh-ключу), домен,
машина деплоя с Docker, `git`, `make`, `jq`, `envsubst`, `python3`. Если на VPS
Telegram заблокирован — свой VLESS-сервер за границей: ссылки `vless://`
вставляются в `.env.vps` как есть (несколько — с автопереключением).

1. **DNS** — A-записи на IP VPS: `gateway.example.com`,
   `turn.gateway.example.com`.
2. **Настройки:**
   ```bash
   cp .env.vps.example .env.vps && chmod 600 .env.vps
   $EDITOR .env.vps   # заполнить все <...>: VPS_HOST, DOMAIN, секреты, выход в Telegram
   ```
3. **Подготовка и защита VPS:** `make vps-prepare` (Docker, swap, экономия
   памяти), `make vps-harden` (обновления безопасности, ssh только по ключу,
   ufw: 22/80/443)
4. **nginx + сертификат:** `make vps-nginx`
5. **Деплой** (из закоммиченного кода): `make vps-deploy`
6. **Проверка:** `make vps-check`, затем `https://gateway.example.com` —
   вход под логином и паролем, выведенными в консоль первого деплоя.

Обновление: `git pull && make vps-deploy`. Логи: `make vps-logs ARGS=api`.
Запуск стека дома за NAT с входом через VPS и перенос прода между домом и
VPS: [`docs/deploy/HOME.md`](docs/deploy/HOME.md).

## Быстрый старт (локально, для разработки)

Требования: Docker, Docker Compose, Go 1.25+, Node 22+.

```bash
cp .env.example .env   # задайте уникальные POSTGRES_PASSWORD и BOT_API_TOKEN
make up                # сборка и запуск всех сервисов
```

### Защищённый транспорт Telegram (S19)

Все DC WebSocket-соединения проходят через основной домен:
`wss://<gateway>/tg/_gw/dc/<1..5>/apiws`. На входе проверяются сессия,
версия пароля, активность аккаунта и актуальное согласие. После отзыва
сессии/блокировки соединение закрывается в пределах примерно 8 секунд.
Cookie шлюза не передаются в Telegram. Прямые DC-поддомены отвечают 410,
raw-TCP DC/passthrough listeners больше не запускаются.

`SOCKS5_ADDR` по-прежнему задаёт выход к Telegram; браузеру нужен только
HTTPS-доступ к основному домену. Для звонков используется отдельный
`turn.<домен>`. DC-поддомены и `DC_REWRITE_*` для нового транспорта не нужны.
При несовместимом обновлении Telegram-worker шлюз закрывает доступ к бандлу
и увеличивает метрику `rewrite_total{result="miss"}`.

Инструкция обновления, привязки бота, проверки восстановления и E2E:
[`docs/HARDENING-S19.md`](docs/HARDENING-S19.md).

### Первый администратор (bootstrap)

У админа нет отдельного сидера-миграции: admin-only эндпоинты (`POST /users`,
одобрение заявок) требуют уже существующего админа. Чтобы разорвать этот круг,
API при первом старте создаёт администратора, если в БД ещё нет ни одного.
Логин вида `admin-<случайный идентификатор>@example.com` и случайный пароль
выводятся в консоль при `make up` / `make vps-deploy`. Их также можно найти в
`docker compose logs api` (на VPS — `make vps-logs ARGS=api`). Сохраните их и
ограничьте доступ к журналу контейнера: он содержит пароль в открытом виде.
Повторный запуск не создаёт новые учётные данные и не меняет существующего
администратора. Имя учётки — `System Administrator`; история паролей и
`password_changed_at` заполняются при создании.

> **Как сменить пароль админа** (варианты, включая доступ через
> `scripts/userctl.sh` и сброс lockout): смотри
> [`docs/gateway-admin.md`](docs/gateway-admin.md).

### Отключение функциональности tg-bot (S11)

Вся интеграция с Telegram-ботом (контейнер `bot`, приватные эндпоинты `/bot/*`,
публикация событий в Redis, крон-рассылки, поле `telegram` в UI) управляется
feature-toggle `TG_BOT_ENABLED`. Значение задаётся на хосте в `.env`:

- `TG_BOT_ENABLED=true` (по умолчанию) — бот включён.
- `TG_BOT_ENABLED=false` — бот и связанный функционал отключены: контейнер `bot`
  не поднимается, `/bot/*` не регистрируются, события не публикуются, поле
  `telegram` скрывается в веб-интерфейсе.

При старте API дефолт с хоста сеется в таблицу `settings` (ключ `telegram.bot`).
После этого значение можно менять на лету через админ-API
`GET/PUT /settings/telegram.bot` (значение `"true"`/`"false"`) без перезапуска —
табличное значение имеет приоритет над env.

После запуска:

| Сервис | URL |
|---|---|
| Gateway (HTTPS) | https://localhost |
| API health | http://localhost:8080/health |
| Prometheus | http://localhost:9090 |
| Grafana (admin/admin) | http://localhost:3001 |
| Loki | http://localhost:3100 |

### Внутренний CA и сертификаты

`make up` сам генерирует TLS-сертификаты. Если `infra/nginx/certs/` содержит
`ca.crt`/`ca.key` (внутренний CA), серверный сертификат подписывается им;
иначе создаётся самоподписанный `CN=localhost` (для быстрых локальных тестов).

Для корпоративного доступа создайте внутренний CA и подпишите сертификат с
SAN IP шлюза (берётся из `DC_REWRITE_IP` в `.env`):

```bash
make ca    # создаёт ca.crt/ca.key и подписывает server.crt; SAN задаётся CA_SAN
docker compose restart nginx
```

**Почему это важно:** браузер разрешает Service Worker (Telegram Web им
пользуется) только на trustworthy-origin. Самоподписанный сертификат к клиенту
SW не регистрируется («An SSL certificate error occurred when fetching the
script»), даже если пользователь обошёл предупреждение для страницы. С
доверенным внутренним CA и страница, и SW работают без ошибок.

Затем установите `infra/nginx/certs/ca.crt` на каждый клиент:

- **Windows**: двойной клик → *Установить сертификат* → *Локальный компьютер* → *Доверенные корневые центры сертификации*.
- **Linux (Debian/Ubuntu)**: `sudo cp ca.crt /usr/local/share/ca-certificates/gateway-ca.crt && sudo update-ca-certificates` (затем перезапустить Chrome/Firefox).
- **Android**: Настройки → Безопасность → Установка сертификатов (или шифровка и учётные данные → Установить сертификат) → файл `ca.crt`.
- **iOS/macOS**: профиль CA (файл в формате `.cer`), в настройках профиля включить полное доверие (для сертификатов корневых ЦС).

После установки открывайте `https://<адрес-шлюза>`
без предупреждений TLS; Service Worker регистрируется нормально.

Для продакшена при наличии публичного домена можно вместо внутреннего CA
использовать Let's Encrypt (certbot) — пути в nginx не меняются.

## Локальная разработка

API:

```bash
cd api
go run ./cmd/api            # слушает :8080
```

Web (Vite dev с прокси `/api` → `localhost:8080`):

```bash
cd web
npm install
npm run dev                 # http://localhost:5173
```

## Команды

```bash
make up          # поднять compose
make down        # остановить
make test        # тесты api + web
make lint        # golangci-lint + eslint
make cert        # self-signed TLS-сертификаты
```

## Безопасность (базовые принципы)

- Серверные сессии в Redis, `HttpOnly` + `Secure` + `SameSite=Strict` cookie.
- Токены не хранятся в LocalStorage.
- TLS 1.3, HSTS, CSP (настраивается в S9).
- Доступ к Telegram Web закрыт без активной сессии и принятых согласий.
