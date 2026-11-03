# Домашний сервер за NAT + VPS как вход (опционально)

> S19: действующая схема DC-доступа — основной домен и
> `/tg/_gw/dc/<id>/apiws`. Старые DC-порты/поддомены из исторических
> инструкций больше не используются. Перед обновлением прочитайте
> [инструкцию S19](/docs/HARDENING-S19.md).


Стек работает дома (мини-ПК или сервер без белого IP), а публичный вход
остаётся на VPS: nginx VPS терминирует TLS и отправляет трафик в frp-туннель.
Удобно, когда дома больше ресурсов, а VPS маленький; прод можно переносить
между домом и VPS вместе с БД. Обзор: [`README.md`](README.md).

Предполагается, что VPS уже настроен по [`VPS.md`](VPS.md) §1–5 (DNS, `.env.vps`,
`vps-prepare`, `vps-nginx`): переключатель трафика ставит `setup-nginx.sh`.

## 1. Домашний стек

На домашнем сервере (он же машина деплоя — скрипты запускаются здесь):

```bash
cp .env.example .env    # секреты, DOMAIN-переменные — как в .env.vps
make up                 # docker-compose.yml
```

Отличия `.env` от `.env.vps`:

| | дом (`.env`) | VPS (`.env.vps`) |
|---|---|---|
| `SOCKS5_ADDR` | ваш SOCKS5 до Telegram (например v2rayA `:20170`) или пусто | `xray:10808` или пусто |
| `COMPOSE_FILE` | не задан | `docker-compose.yml:docker-compose.vps.yml` |
| `XRAY_*`, `VPS_*`, `INGRESS_HOME_*` | не нужны | нужны |
| `LOG_FILE` | `/logs/app.log` (promtail → Loki) | пусто (stdout) |

`GATEWAY_URL`, `DC_REWRITE_DOMAIN`, `DC_BRIDGE_WSS_*`, `TURN_URLS`,
`BOT_TOKEN` должны совпадать (тот же домен и бот). Пароли БД могут
различаться: перенос идёт дампом, а не копией тома.

## 2. frp-туннель

На VPS — `frps`, дома — `frpc` ([frp](https://github.com/fatedier/frp)).
Пример туннелей в `frpc.toml` (`remotePort` — это `INGRESS_HOME_*` в `.env.vps`):

```toml
serverAddr = "<VPS_IP>"
serverPort = 7000
auth.token = "<секрет frps>"

[[proxies]]          # веб-вход: nginx домашнего стека :443
name = "gateway-web"
type = "tcp"
localIP = "127.0.0.1"
localPort = 443
remotePort = 15443   # INGRESS_HOME_WEB=127.0.0.1:15443

[[proxies]]          # DC-мост 1 (повторить для 2..5: 21002->20012 и т.д.)
name = "gateway-dc1"
type = "tcp"
localIP = "127.0.0.1"
localPort = 21001
remotePort = 20011   # INGRESS_HOME_DC1=127.0.0.1:20011

[[proxies]]          # TURN: telegram-proxy 127.0.0.1:3478
name = "gateway-turn"
type = "tcp"
localIP = "127.0.0.1"
localPort = 3478
remotePort = 36005   # INGRESS_HOME_TURN=127.0.0.1:36005
```

На VPS порты frps не открывайте наружу (nginx ходит к ним по `127.0.0.1`);
в `frps.toml` ограничьте `allowPorts` этими портами.

Проверка с VPS: `curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:15443/`.

## 3. Переключение трафика

```bash
make ingress-status   # vps или home
make ingress-home     # трафик -> домашний стек (проверяет, что он отвечает)
make ingress-vps      # трафик -> стек на VPS
```

Переключение меняет только upstream'ы nginx VPS (`nginx -t` → `reload`, при
ошибке — откат), без простоя. Данные **не переносятся**: у каждого стека своя
БД. Чтобы переехать вместе с данными — §4.

## 4. Перенос прода с данными

```bash
make move-to-vps      # дом -> VPS
make move-to-home     # VPS -> дом
```

`scripts/vps/move.sh`: остановка `api telegram-proxy bot` на обоих хостах →
`pg_dump -Fc` источника (копия в `.data/move/`) → `pg_restore --clean
--single-transaction` на приёмнике (при ошибке БД приёмника не меняется) →
старт приёмника, ожидание `/health` → переключение трафика.

- Простой — время дампа/восстановления (минуты).
- Redis (сессии) не переносится: пользователи один раз перелогинятся.
- БД источника не удаляется; откат — перенос в обратную сторону.
- Перед `move-to-home` обновите домашний стек до той же версии кода
  (`git pull && make up`) — миграции должны совпадать.
- Перед `move-to-vps` — `make vps-deploy` (стек на VPS здоров, `make vps-check`).

После `move-to-vps` дома остаются postgres/redis/web/nginx и мониторинг;
освободить ресурсы: `docker compose stop`. После `move-to-home` на VPS
остаются postgres/redis/xray: `ssh <VPS_HOST> 'cd /opt/telegram-gateway && docker compose stop'`.

## 5. Аварии

| Ситуация | Действие |
|---|---|
| упал стек на VPS, nginx VPS жив | `make up` дома → `make ingress-home` (данные — на момент последнего переноса) |
| пропал интернет/питание дома | `make vps-deploy` (если стек на VPS не запущен) → `make ingress-vps` |
| упал VPS целиком | публичного входа нет: поднять VPS или новый по [`VPS.md`](VPS.md) |

После аварийного переключения без переноса данные расходятся: верните их
`make move-to-*` в сторону того хоста, чьи данные новее.
