# Развёртывание прод-шлюза

Два варианта размещения; их можно совмещать и переносить прод между ними
вместе с базой данных.

| | **VPS** (основной) | **Домашний сервер за NAT** (опционально) |
|---|---|---|
| Где работает стек | на VPS | дома (мини-ПК, сервер без белого IP) |
| Публичный вход | nginx на VPS (443, Let's Encrypt) | тот же nginx на VPS → frp-туннель домой |
| Ресурсы | от 1 vCPU / 1 ГБ ОЗУ | сколько есть |
| Compose | `docker-compose.yml` + `docker-compose.vps.yml` | `docker-compose.yml` |
| Env | `.env.vps` (на VPS лежит как `<VPS_DIR>/.env`) | `.env` |
| Мониторинг | выключен (профиль `monitoring`) | prometheus, grafana, loki, promtail |
| Выход в Telegram | напрямую или контейнер `xray` (VLESS+REALITY) | `SOCKS5_ADDR` (любой SOCKS5) |
| Документ | [`VPS.md`](VPS.md) | [`HOME.md`](HOME.md) |

Домен, сертификаты и DNS при переносе не меняются: публичный вход всегда на
VPS, переключается только то, куда nginx VPS отправляет трафик.

## Схема

```
                     Браузер / Telegram Web
                               │ 443: DOMAIN, dc1..5.DOMAIN, turn.DOMAIN
                               ▼
VPS   nginx stream :443 (SNI) ──► nginx http 127.0.0.1:4443 (TLS Let's Encrypt)
                               │
               upstream'ы webtgk_up, tgk_dc1..5, tgk_turn
               conf.d/zz-webtgk-target.conf ──► vps.*.conf | home.*.conf
                               │
     ┌────────── vps ──────────┴──────────── home ─────────────┐
     ▼                                                         ▼
127.0.0.1:16443, 21001-21005, 13478                 порты frps (INGRESS_HOME_*)
docker-compose.vps.yml на VPS                       frp-туннель → домашний стек
     │                                                         │
     ▼ (напрямую или xray → ваш VLESS-сервер)                  ▼ (SOCKS5_ADDR)
                            Telegram
```

Порты двух таргетов не пересекаются, поэтому стек на VPS и туннель frp могут
работать одновременно — трафик получает тот, на кого указывает переключатель.

## Команды

Все настройки VPS — в `.env.vps` (шаблон [`../../.env.vps.example`](../../.env.vps.example)).
Команды выполняются на машине с Docker и этим репозиторием, не на VPS.

| Команда | Что делает |
|---|---|
| `make vps-prepare` | Docker, swap 1 ГБ, лимит журнала, отключение лишних служб на VPS |
| `make vps-harden` | обновления безопасности, ssh только по ключу, ufw, sysctl |
| `make vps-nginx` | nginx + сертификат Let's Encrypt для домена (чистый VPS) |
| `make vps-deploy` | собрать образы локально, отправить на VPS, поднять стек |
| `make vps-check` | `/health`, выход в Telegram, память VPS |
| `make vps-status` / `make vps-logs ARGS=<сервис>` | состояние / логи |
| `make ingress-status` / `ingress-vps` / `ingress-home` | куда идёт трафик / переключить |
| `make move-to-vps` / `move-to-home` | перенос прода с БД между домом и VPS |

Скрипты: `scripts/vps/` (общие функции и переменные — `scripts/vps/lib.sh`).
