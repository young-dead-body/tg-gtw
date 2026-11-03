# Прод-шлюз на VPS

> S19: действующая схема DC-доступа — основной домен и
> `/tg/_gw/dc/<id>/apiws`. Старые DC-порты/поддомены из исторических
> инструкций больше не используются. Перед обновлением прочитайте
> [инструкцию S19](/docs/HARDENING-S19.md).


Пошаговое развёртывание на VPS от 1 vCPU / 1 ГБ ОЗУ. Обзор и схема:
[`README.md`](README.md). Команды выполняются **на машине деплоя** (ноутбук
или сервер с Docker и клоном репозитория), если не сказано иное.

## 0. Что понадобится

| | |
|---|---|
| VPS | Ubuntu 22.04+ / Debian 12+, от 1 vCPU / 1 ГБ ОЗУ / 10 ГБ диска, публичный IPv4, root-доступ по ssh-ключу |
| Домен | свой домен или поддомен, например `gateway.example.com` |
| Машина деплоя | Linux/macOS с Docker (сборка образов), `git`, `make`, `ssh`, `jq`, `envsubst` (пакет `gettext`), `python3` |
| Выход в Telegram | если VPS в стране, где Telegram заблокирован, — свой сервер VLESS+REALITY за её пределами (XHTTP или TCP) |

## 1. DNS

Семь A-записей на IP VPS:

| Имя | Зачем |
|---|---|
| `gateway.example.com` | веб-вход: админка, API, Telegram Web |
| `dc1.gateway.example.com` … `dc5.gateway.example.com` | DC-мост Telegram Web (вход по QR, MTProto через 443) |
| `turn.gateway.example.com` | TURN-релей для звонков |

Пример для reg.ru: [`../dns-regru.md`](../dns-regru.md). Проверка:
`getent hosts dc1.gateway.example.com`.

## 2. Окружение `.env.vps`

```bash
git clone <repo> telegram-gateway && cd telegram-gateway
cp .env.vps.example .env.vps && chmod 600 .env.vps
$EDITOR .env.vps
```

Заполнить все `<...>` (деплой откажется работать, пока они остались):

- `VPS_HOST`, `VPS_SSH_KEY` — доступ к VPS;
- `DOMAIN`, `ACME_EMAIL` и производные: `GATEWAY_URL`, `DC_REWRITE_DOMAIN`,
  `DC_BRIDGE_WSS_1..5`, `TURN_URLS`;
- секреты — `openssl rand -hex 24` (значения из примеров деплой не примет);
- выход в Telegram:
  - **напрямую** (VPS вне блокировок): `COMPOSE_PROFILES=`, `SOCKS5_ADDR=`,
    `BOT_HTTPS_PROXY=`, блок `XRAY_*` не нужен;
  - **через xray**: `COMPOSE_PROFILES=xray`, `SOCKS5_ADDR=xray:10808`,
    `BOT_HTTPS_PROXY=socks5://xray:10808` и ссылки вашего сервера как есть:
    `XRAY_LINK_1='vless://...'` (в одинарных кавычках). Поддерживаются
    `type=tcp|xhttp|ws|grpc`, `security=reality|tls`. Запасные узлы —
    `XRAY_LINK_2`, `XRAY_LINK_3`, …: xray раз в минуту проверяет каждый узел
    и ведёт трафик через живой с наименьшей задержкой (если все молчат —
    через первый). Конфиг собирает `scripts/vps/xray-config.py` (нужен
    `python3` на машине деплоя).

`.env.vps` и отрендеренный `infra/xray/config.json` в git не попадают.

## 3. Подготовка VPS

```bash
make vps-prepare ARGS=--dry-run   # показать план
make vps-prepare
```

Идемпотентно: ставит Docker и compose-плагин (если нет), создаёт swap 1 ГБ
(`vm.swappiness=10`), ограничивает журнал systemd (50 МБ диск / 16 МБ ОЗУ),
маскирует ненужные на сервере службы (fwupd, ModemManager, udisks2, upower,
multipathd, networkd-dispatcher), отключает тяжёлые motd-скрипты
(`check-new-release` при каждом ssh-входе съедает ~100 МБ) и создаёт
`VPS_DIR`.

### Защита

```bash
make vps-harden ARGS=--dry-run
make vps-harden
```

`scripts/vps/harden.sh` (идемпотентно):

| Что | Как |
|---|---|
| обновления безопасности | `unattended-upgrades` (ежедневно) + сразу ставит накопившиеся; о нужной перезагрузке сообщает, сам не перезагружает |
| ssh | `sshd_config.d/01-gateway-hardening.conf`: только ключи, root только по ключу, `MaxAuthTries 3`, `LoginGraceTime 30`, без X11. Пароли отключаются, только если вход по ключу уже работает; после reload проверяется новое подключение, при неудаче — откат |
| ufw | deny incoming; `22/tcp` — `limit` (не больше 6 новых подключений за 30 с с IP, против перебора), `80`, `443`, плюс `UFW_EXTRA_ALLOW` из `.env.vps`. Существующие правила не удаляются |
| sysctl | запрет ICMP-redirect и source routing, SYN cookies (`/etc/sysctl.d/91-gateway-hardening.conf`) |

Docker публикует порты **в обход ufw**, поэтому все порты стека привязаны к
`127.0.0.1` (`docker-compose.vps.yml`). Скрипты деплоя держат одно
ssh-подключение (ControlMaster) и не упираются в `limit` на 22.
fail2ban не ставится: при входе только по ключу перебор безопасен, а ufw
`limit` гасит шум без лишних ~30 МБ ОЗУ.

После `make vps-harden`, если он сообщил «нужна перезагрузка»:
`ssh <VPS_HOST> reboot`, затем `make vps-check` (все сервисы стартуют сами).

## 4. Почему так: ограничения 1 ГБ ОЗУ

Пример замера на Ubuntu 26.04 с 1 ГБ до подготовки: `free` показывает ~400 МБ
«used», из них процессы ~130 МБ (journald 57, dockerd 55, fwupd 31, containerd
30, networkd-dispatcher 22…), ядро ~75 МБ, остальное — запас ядра, который
`free` не считает доступным. После `vps-prepare` освобождается ~110 МБ.

| Ограничение | Решение |
|---|---|
| сборка Go/npm на 1 ГБ падает по OOM | образы собираются на машине деплоя, на VPS едут через `docker save \| ssh docker load` |
| мониторинг ~210 МБ (loki, grafana, promtail, prometheus) | профиль `monitoring`, по умолчанию выключен |
| без swap OOM-killer снимает postgres | swap 1 ГБ |
| неограниченный рост контейнеров | `mem_limit`, `GOMEMLIMIT`, Postgres `shared_buffers=32MB`, Redis `maxmemory 48mb` |
| логи без ротации | json-file 3×10 МБ, файловый лог api выключен |

Бюджет стека (в простое / потолок `mem_limit`), МБ: postgres 25/192, redis
14/64, api 18/128, telegram-proxy 18/192, bot ~10/64, web 13/32, nginx 19/64,
xray ~25/64 — **~140 в простое**. Под нагрузкой растёт в основном
`telegram-proxy` (WebSocket-соединения Telegram Web). При упоре в лимит
перезапускается контейнер, а не падает хост.

## 5. nginx и сертификат

```bash
make vps-nginx ARGS=--check   # DNS и конфликты портов, без изменений
make vps-nginx
```

`scripts/vps/setup-nginx.sh`:

1. проверяет, что все 7 имён указывают на IP VPS и что 80/443 не заняты
   ничем, кроме nginx, а в nginx нет чужих сайтов на 443;
2. ставит `nginx`, `libnginx-mod-stream`, `certbot`, добавляет `stream {}` в
   `nginx.conf`;
3. выпускает один сертификат Let's Encrypt на 7 имён (`certbot --webroot`,
   автопродление `certbot.timer` с `reload nginx`);
4. ставит конфиги из `infra/vps/nginx/`: SNI-маршрутизатор на 443
   (`turn.DOMAIN` → TURN, остальное → `127.0.0.1:4443`), веб-вход и DC-мост,
   переключатель таргета (по умолчанию `vps`). При ошибке `nginx -t` откатывает.

### VPS с другими сайтами

Если на 443 уже есть сайты, автоматическая настройка остановится. Вручную:

1. перевести существующие `listen 443 ssl` на `listen 127.0.0.1:4443 ssl proxy_protocol`
   (+ `set_real_ip_from 127.0.0.1; real_ip_header proxy_protocol;`);
2. отрендерить шаблоны `infra/vps/nginx/site/*.template` и `targets/*` с
   вашими значениями (`envsubst '${DOMAIN}'`) и положить, как это делает
   `setup-nginx.sh` (sites-enabled, streams-enabled, `webtgk-targets/` + два
   symlink'а `zz-webtgk-target.conf`);
3. выпустить сертификат на 7 имён любым способом и `nginx -t && systemctl reload nginx`.

## 6. Деплой стека

```bash
git add -A && git commit -m "..."   # деплой требует чистого git-дерева
make vps-deploy                     # build + отправка + up
make vps-deploy ARGS=--no-build     # только конфиги/перезапуск
```

`scripts/vps/deploy.sh`:

1. гарды: чистое дерево, нет `<...>` и паролей из примеров в `.env.vps`,
   заполнены `XRAY_*` при профиле `xray`;
2. сборка `api migrate bot web telegram-proxy` локально;
3. список образов VPS-профиля (`docker compose config --images`); на VPS
   отправляются только образы с другим ID;
4. сборка конфига xray из ссылок (`xray-config.py`) и проверка `xray -test`;
5. загрузка в `VPS_DIR`: compose-файлы, `infra/nginx` (конфиг, шаблоны,
   самоподписанный сертификат внутреннего TLS-хопа — `make cert`), конфиги
   мониторинга, `.env` (600), `REVISION`;
6. `docker compose up -d` (миграции с гардом `MIGRATE_EXPECT_DB`);
7. проверки: `api /health`, выход к `web.telegram.org`, память.

После первого деплоя: сохраните выведенные в консоль `BOOTSTRAP_ADMIN_EMAIL` и
`BOOTSTRAP_ADMIN_PASSWORD`, откройте `https://gateway.example.com` и войдите под
ними. Затем откройте Telegram Web и войдите по QR-коду.

## 7. Эксплуатация

| Задача | Команда |
|---|---|
| обновить | `git pull` → `make vps-deploy` |
| статус / логи | `make vps-status`, `make vps-logs ARGS=api` |
| здоровье и память | `make vps-check` |
| мониторинг (+~210 МБ) | `COMPOSE_PROFILES=xray,monitoring` в `.env.vps` → `make vps-deploy ARGS=--no-build`; Grafana: `ssh -L 3001:127.0.0.1:3001 <VPS_HOST>` → `http://localhost:3001`. Логов api в Loki не будет: на VPS api пишет только в stdout |
| бэкап БД | автоматически (`BACKUP_CRON_INTERVAL`, volume `backups`); вручную: `ssh <VPS_HOST> 'cd /opt/telegram-gateway && docker compose exec -T postgres pg_dump -U tg -Fc telegram_gateway' > backup.dump` |
| смена пароля админа | [`../gateway-admin.md`](../gateway-admin.md) |

## 8. Диагностика

| Симптом | Что проверить |
|---|---|
| `xray -test` / `xray-config` не прошёл | ссылки `XRAY_LINK_N`: целиком в одинарных кавычках, схема `vless://`, есть `uuid@host:port` |
| `vps-check`: web.telegram.org FAIL | `make vps-logs ARGS=xray`; с xray — ссылки и `docker compose logs xray` (observatory пишет, какие узлы живы), без xray — доступен ли Telegram с VPS (`curl -I https://web.telegram.org` на VPS) |
| 502 на домене | `make ingress-status`; на VPS `curl -sk https://127.0.0.1:16443/` |
| QR-код не загружается | DNS `dc1..5`, на VPS `ss -ltn \| grep 2100`, `make vps-logs ARGS=nginx` |
| контейнер перезапускается | `docker inspect <c> -f '{{.State.OOMKilled}}'` → поднять `mem_limit` в `docker-compose.vps.yml` |
| хост тормозит | `free -m`, `vmstat 1`: постоянный swap-in/out → выключить бота/мониторинг или взять 2 ГБ |
| бот молчит | `BOT_REPLICAS=1`, `TG_BOT_ENABLED=true`, `BOT_TOKEN`; Bot API идёт через `BOT_HTTPS_PROXY` |
