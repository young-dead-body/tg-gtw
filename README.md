# Telegram Gateway

Шлюз с веб-панелью, защищённым доступом к Telegram Web и необязательным ботом.
Стандартное развёртывание выполняется **на том VPS, где работают контейнеры**.
Нужны только [docker-compose.yml](docker-compose.yml), [.env.example](.env.example)
и [scripts/install.sh](scripts/install.sh). Полная инструкция:
[развёртывание и эксплуатация](docs/deploy/README.md).

Быстрый запуск на чистом Ubuntu/Debian VPS:

```bash
git clone <адрес-этого-репозитория> telegram-gateway
cd telegram-gateway
sudo ./scripts/install.sh --domain gateway.example.com --email admin@example.com
```

Перед запуском направьте A-запись домена на IPv4 VPS и откройте входящие
TCP 80/443. Установщик создаёт `.env`, генерирует секреты сервисов,
запускает контейнеры, выдаёт сертификат Let's Encrypt и печатает случайные
логин и пароль первого администратора. Пароль администратора в `.env`
не хранится. При повторном запуске учётка сохраняется; смена пароля:
[scripts/userctl.sh](scripts/userctl.sh) и [инструкция](docs/gateway-admin.md).

При прямом доступе VPS к Telegram оставьте `SOCKS5_ADDR` и
`BOT_HTTPS_PROXY` пустыми. Основные функции шлюза и Telegram Web работают
через исходящие соединения VPS. Для встроенного TURN-релея звонков нужен
SOCKS5 с UDP ASSOCIATE; при прямом выходе релей выключен. Подробнее о
сетевых проверках и звонках — в инструкции развёртывания.
