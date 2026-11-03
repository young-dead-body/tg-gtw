# Развёртывание Telegram Gateway на VPS

## Что понадобится

- Ubuntu/Debian VPS с публичным IPv4, root/sudo, свободными TCP 80 и 443.
  Рекомендуется от 2 ГБ ОЗУ; сборка Go и frontend может потребовать больше
  памяти, чем работа готовых контейнеров.
- Домен, например `gateway.example.com`: A-запись ведёт на IPv4 VPS.
  Удалите неверную AAAA-запись, если IPv6 на VPS не работает.
- Исходящий TCP 443 к `web.telegram.org`, `*.web.telegram.org` и, для
  бота, `api.telegram.org`. Для установки также нужны Docker Registry,
  репозитории пакетов и Let's Encrypt. У клиентов должен открываться
  HTTPS вашего домена. Домены DC1–DC5 для текущего транспорта не нужны.
- Открытые входящие TCP 80 (ACME и перенаправление на HTTPS), 443 (шлюз).
  Порт 18090 привязан только к `127.0.0.1`; PostgreSQL и Redis наружу
  не публикуются. На VPS с другим веб-сервером сначала освободите 80/443.

Проверьте DNS до установки:

```bash
getent ahostsv4 gateway.example.com
curl -I https://web.telegram.org
curl -I https://api.telegram.org
```

`api.telegram.org` может вернуть HTTP 404/401: это подтверждает соединение.
Таймаут, DNS или TLS ошибка означают, что прямой выход не готов.

## Установка

```bash
git clone <адрес-репозитория> telegram-gateway
cd telegram-gateway
sudo ./scripts/install.sh --domain gateway.example.com --email admin@example.com
```

Скрипт запускается **на VPS**, создаёт `.env` из единственного шаблона,
заполняет случайные `POSTGRES_PASSWORD` и `BOT_API_TOKEN`, ставит Docker
Compose и certbot через apt при необходимости, собирает и запускает стек.
Для первой выдачи Let's Encrypt nginx временно использует самоподписанный
сертификат. После ACME-проверки скрипт подменяет его настоящим и
перезагружает nginx. Сертификат обновляется ежедневно через
`/etc/cron.d/telegram-gateway-cert`; его deploy hook вызывает тот же
`scripts/install.sh --reload-cert`. Права на `.env` и приватный ключ — 0600.

В конце первой установки выводятся `BOOTSTRAP_ADMIN_EMAIL` и
`BOOTSTRAP_ADMIN_PASSWORD`. Сохраните их и откройте
`https://gateway.example.com`. API создаёт администратора только при
отсутствии админов в БД. Повторная установка не меняет его пароль; если
вывод утрачен, смотрите `docker compose logs api` или
[сбросьте пароль](../gateway-admin.md). Лог контейнера содержит исходный
пароль: доступ к Docker и логам давайте только доверенным администраторам.

После установки:

```bash
docker compose ps
curl -fsS http://127.0.0.1:18090/health
curl -I https://gateway.example.com
docker compose logs --tail=100 telegram-proxy
```

В панели войдите под созданным админом, добавьте пользователей и проверьте
Telegram Web: QR-вход, сообщения и загрузку медиа. Установщик добавляет
`https://web.telegram.org` в пул upstream-адресов только если пул пуст.
Админ может управлять им в панели.

## Прямой доступ к Telegram и звонки

При `SOCKS5_ADDR=` прокси Telegram Web и его WebSocket-соединения с DC
открывают прямые исходящие соединения VPS. Бот при пустом
`BOT_HTTPS_PROXY` тоже обращается к Telegram напрямую. Для доступа
пользователя нужен только основной HTTPS-домен шлюза. Работа зависит от
доступности Telegram именно **из сети контейнеров**, DNS и исходящего TCP 443:
успешный `curl` на хосте полезен, но после установки проверьте реальный
QR-вход и сообщения через браузер.

Встроенный TURN-релей реализован с обязательным SOCKS5 UDP ASSOCIATE.
Поэтому при прямом доступе оставьте `TURN_ADDR` пустым. Голосовые и
видеозвонки могут проходить через собственные ICE/TURN-серверы Telegram,
но из сетей с блокировками или строгим NAT они могут зависать на
«Соединение…». Полную работоспособность звонков при прямом выходе
обещать нельзя. Для гарантированного релея потребуется отдельный SOCKS5
с UDP ASSOCIATE, публичный TURN endpoint и его TLS маршрутизация; эта
конфигурация не входит в стандартную установку.

Если прямой доступ исчезнет, задайте доступный контейнерам `SOCKS5_ADDR`
в `.env` и перезапустите стек через установщик. Для бота отдельно задайте
`BOT_HTTPS_PROXY=socks5://<host>:<port>`. Адрес `127.0.0.1` внутри контейнера
означает сам контейнер; используйте имя DNS/адрес прокси, доступный из
Compose-сети.

## Необязательный бот

Получите токен у @BotFather. В `.env` установите:

```dotenv
TG_BOT_ENABLED=true
BOT_TOKEN=<токен от BotFather>
COMPOSE_PROFILES=bot
```

Затем `sudo ./scripts/install.sh`. Для выключения задайте
`TG_BOT_ENABLED=false`, очистите `COMPOSE_PROFILES` и повторите установку.
Значение переключателя также хранится в таблице настроек; если оно уже
изменялось в панели, проверьте там `telegram.bot`.

## Обновление, остановка, диагностика

```bash
git pull
sudo ./scripts/install.sh
docker compose ps
docker compose logs --tail=200 api telegram-proxy nginx
docker compose down                 # остановить, сохранив данные
```

Повторный запуск сохраняет `.env`, существующий сертификат и Docker volumes.
Не используйте `docker compose down -v`: эта команда удаляет БД, Redis и
резервные копии. Для изменения настроек редактируйте `.env`, затем
повторно запускайте установщик. Секрет БД после создания volume не меняйте
без отдельной процедуры смены пароля в PostgreSQL.

Если ACME не проходит, проверьте DNS, входящий 80, Cloudflare proxy и
`docker compose logs nginx`; установщик можно повторить после исправления.
Проверка обновления сертификата: `sudo certbot renew --dry-run`.
Если API не отвечает, проверьте `docker compose logs migrate api postgres`.
Если Telegram Web недоступен, проверьте `docker compose logs telegram-proxy`,
DNS и исходящий TCP 443 из VPS; затем попробуйте вход в браузере без старого
Service Worker (очистите данные сайта). Для ограниченного VPS проверьте
`free -h` и `docker stats`.

## Данные и восстановление

PostgreSQL, Redis и автоматические SQL-дампы находятся в Docker volumes.
API делает дамп раз в `BACKUP_CRON_INTERVAL` (по умолчанию 24 часа) и
хранит `BACKUP_KEEP` копий в volume `backups`. Это тот же VPS:
для защиты от его потери регулярно копируйте дампы на отдельный хост.
Создать проверяемый дамп вручную:

```bash
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-acl' > gateway.sql
test -s gateway.sql
```

Восстанавливайте только в остановленный стек той же версии схемы.
Сначала сохраните текущую БД. Следующие команды удаляют текущую схему
на целевой машине; выполняйте их только после проверки дампа на отдельном
экземпляре:

```bash
docker compose stop api telegram-proxy
docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"'
docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < gateway.sql
docker compose up -d
```

Проверьте вход и данные после восстановления. Для паролей и пользователей используйте
[инструкцию администратора](../gateway-admin.md).
