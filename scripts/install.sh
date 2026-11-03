#!/usr/bin/env bash
# Install or update Telegram Gateway on the machine that runs the containers.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export COMPOSE_FILE="$ROOT/docker-compose.yml"

die() { echo "Ошибка: $*" >&2; exit 1; }
get_env() { sed -n "s/^$1=//p" .env | tail -n 1; }

if [[ "${1:-}" == "--reload-cert" ]]; then
  [[ ${EUID} -eq 0 ]] || die "для обновления сертификата нужны права root"
  domain="$(get_env DOMAIN)"
  install -m 644 "/etc/letsencrypt/live/$domain/fullchain.pem" infra/nginx/certs/server.crt
  install -m 600 "/etc/letsencrypt/live/$domain/privkey.pem" infra/nginx/certs/server.key
  docker compose exec -T nginx nginx -s reload
  exit
fi

domain=""
email=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) domain="${2:-}"; shift 2 ;;
    --email) email="${2:-}"; shift 2 ;;
    -h|--help)
      echo "Использование: sudo ./scripts/install.sh --domain gateway.example.com --email admin@example.com"
      echo "Повторный запуск без параметров обновляет уже установленный шлюз."
      exit 0 ;;
    *) die "неизвестный параметр: $1" ;;
  esac
done

[[ ${EUID} -eq 0 ]] || die "запустите через sudo/root"
[[ -f .env ]] || { cp .env.example .env; chmod 600 .env; }
[[ -n "$domain" ]] || domain="$(get_env DOMAIN)"
[[ -n "$email" ]] || email="$(get_env ACME_EMAIL)"
[[ "$domain" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}$ ]] || die "укажите публичный домен через --domain"
[[ "$email" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] || die "укажите email для Let's Encrypt через --email"

if ! command -v python3 >/dev/null; then
  command -v apt-get >/dev/null || die "установите Python 3"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y python3
fi

# Python writes only missing service secrets and preserves user settings on updates.
python3 - "$domain" "$email" <<'PY'
import pathlib, secrets, sys
path = pathlib.Path(".env")
lines = path.read_text().splitlines()
values = {"DOMAIN": sys.argv[1], "ACME_EMAIL": sys.argv[2], "COMPOSE_FILE": "docker-compose.yml"}
current = dict(line.split("=", 1) for line in lines if "=" in line and not line.startswith("#"))
for key in ("POSTGRES_PASSWORD", "BOT_API_TOKEN"):
    if not current.get(key):
        values[key] = secrets.token_hex(32)
for key, value in values.items():
    found = False
    for index, line in enumerate(lines):
        if line.startswith(key + "="):
            lines[index] = key + "=" + value
            found = True
            break
    if not found:
        lines.append(key + "=" + value)
path.write_text("\n".join(lines) + "\n")
path.chmod(0o600)
PY

if [[ -n "$(get_env TURN_ADDR)" && -z "$(get_env SOCKS5_ADDR)" ]]; then
  die "TURN-релей требует SOCKS5_ADDR с UDP ASSOCIATE; для прямого выхода очистите TURN_ADDR"
fi
if [[ "$(get_env TG_BOT_ENABLED)" == "true" ]]; then
  [[ -n "$(get_env BOT_TOKEN)" && "$(get_env COMPOSE_PROFILES)" == *bot* ]] ||
    die "для бота задайте BOT_TOKEN и COMPOSE_PROFILES=bot в .env"
fi

if ! docker compose version >/dev/null 2>&1; then
  command -v apt-get >/dev/null || die "установите Docker Engine и Compose v2"
  apt-get update
  if apt-cache show docker-compose-v2 >/dev/null 2>&1; then
    compose_pkg=docker-compose-v2
  elif apt-cache show docker-compose-plugin >/dev/null 2>&1; then
    compose_pkg=docker-compose-plugin
  else
    die "в apt нет Compose v2; установите Docker Engine с Compose plugin"
  fi
  DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io "$compose_pkg"
  systemctl enable --now docker
fi
if ! command -v certbot >/dev/null || ! command -v crontab >/dev/null || ! command -v curl >/dev/null || ! command -v openssl >/dev/null; then
  command -v apt-get >/dev/null || die "установите certbot"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y certbot cron curl openssl
fi
systemctl enable --now cron
mkdir -p infra/nginx/certs infra/certbot/.well-known/acme-challenge
chmod 755 infra/certbot infra/certbot/.well-known infra/certbot/.well-known/acme-challenge
if [[ ! -s infra/nginx/certs/server.crt || ! -s infra/nginx/certs/server.key ]]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj "/CN=$domain" -keyout infra/nginx/certs/server.key \
    -out infra/nginx/certs/server.crt >/dev/null 2>&1
  chmod 600 infra/nginx/certs/server.key
fi

docker compose config --quiet
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker compose up -d --build --remove-orphans
for attempt in {1..60}; do
  if curl -fsS http://127.0.0.1:18090/health >/dev/null; then break; fi
  [[ "$attempt" -lt 60 ]] || die "API не ответил: docker compose logs api"
  sleep 2
done

# A fresh database needs at least one Telegram Web backend.
docker compose exec -T postgres sh -c 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
INSERT INTO proxy_pool (url, status, priority)
SELECT 'https://web.telegram.org', 'active', 1
WHERE NOT EXISTS (SELECT 1 FROM proxy_pool);
SQL

echo "Учётные данные первого администратора (выводятся только при создании):"
docker compose logs --since "$started" --no-color api |
  sed -n '/BOOTSTRAP_ADMIN_\(EMAIL\|PASSWORD\)=/p'

certbot certonly --webroot -w "$ROOT/infra/certbot" -d "$domain" \
  --cert-name "$domain" --email "$email" --agree-tos --non-interactive \
  --keep-until-expiring
"$ROOT/scripts/install.sh" --reload-cert

# Copy the certificate even if certbot's own timer renewed it first.
printf '17 3 * * * root certbot renew --cert-name %s --quiet && %s/scripts/install.sh --reload-cert\n' \
  "$domain" "$ROOT" > /etc/cron.d/telegram-gateway-cert
chmod 644 /etc/cron.d/telegram-gateway-cert

echo
echo "Шлюз: https://$domain"
echo "Статус: docker compose ps"
