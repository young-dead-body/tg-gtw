#!/usr/bin/env bash
# Внешний вход шлюза на VPS: системный nginx + Let's Encrypt (docs/deploy/VPS.md §5).
# Для ЧИСТОГО VPS, где 80/443 больше никто не занимает. Если на VPS уже есть
# свои сайты на 443 — см. docs/deploy/VPS.md «VPS с другими сайтами».
#
#   1. проверка DNS: DOMAIN, turn.DOMAIN -> один IP;
#   2. apt: nginx, libnginx-mod-stream, certbot; stream {} в nginx.conf;
#   3. временный :80 для ACME -> certbot --webroot (один сертификат на 2 имени,
#      автопродление certbot.timer + reload nginx);
#   4. конфиги из infra/vps/nginx/: SNI-маршрутизатор :443 (stream),
#      веб-вход и DC-мост на 127.0.0.1:4443, переключатель таргета (vps).
#
# Usage: setup-nginx.sh [--check]   (--check: только проверки, без изменений)
set -euo pipefail
source "$(dirname "$0")/lib.sh"

CHECK_ONLY=0
[ "${1:-}" = "--check" ] && CHECK_ONLY=1

DOMAIN="$(env_get "$VPS_ENV" DOMAIN)"
ACME_EMAIL="$(env_get "$VPS_ENV" ACME_EMAIL)"
[[ -n "$DOMAIN" && "$DOMAIN" != *"<"* ]] || die "DOMAIN не задан в $VPS_ENV."
[[ -n "$ACME_EMAIL" && "$ACME_EMAIL" != *"<"* ]] || die "ACME_EMAIL не задан в $VPS_ENV."
NAMES=("$DOMAIN" "turn.$DOMAIN")
SRC="$PROD_DIR/infra/vps/nginx"

# --- 1. DNS ------------------------------------------------------------------
VPS_IP="$(vps_ssh "curl -4 -s -m 10 https://ifconfig.me || true")"
if [ -z "$VPS_IP" ]; then
  VPS_IP="$(getent ahostsv4 "$DOMAIN" | awk 'NR==1{print $1}')"
  log "публичный IP VPS не определился (ifconfig.me), сверяю имена с $DOMAIN -> ${VPS_IP:-?}"
else
  log "публичный IPv4 VPS: $VPS_IP"
fi
dns_ok=1
for n in "${NAMES[@]}"; do
  ip="$(getent ahostsv4 "$n" | awk 'NR==1{print $1}')"
  if [ "$ip" = "$VPS_IP" ]; then
    log "DNS $n -> $ip: OK"
  else
    log "DNS $n -> ${ip:-нет записи}: ожидается $VPS_IP"; dns_ok=0
  fi
done
[ "$dns_ok" = 1 ] || die "создайте A-записи (docs/deploy/VPS.md §1) и дождитесь их распространения."

# --- конфликты на 80/443 -----------------------------------------------------
conflicts="$(vps_ssh "ss -ltnpH '( sport = :80 or sport = :443 )' | grep -v '\"nginx\"' || true")"
[ -z "$conflicts" ] || die "порты 80/443 на VPS заняты не nginx:
$conflicts"
foreign443="$(vps_ssh "command -v nginx >/dev/null && nginx -T 2>/dev/null | grep -nE '^\s*listen\s+(\S+:)?443[ ;]' | grep -v 'ssl_preread' || true")"
if vps_ssh "test -f /etc/nginx/streams-enabled/telegram-gateway.conf"; then
  foreign443=""   # повторный запуск: 443 уже наш stream-маршрутизатор
fi
[ -z "$foreign443" ] || die "в nginx VPS уже есть сайты на 443 — автоматическая настройка их сломает.
См. docs/deploy/VPS.md «VPS с другими сайтами». Найдено:
$foreign443"

[ "$CHECK_ONLY" = 1 ] && { log "проверки пройдены (--check, изменений нет)."; exit 0; }

# --- рендер конфигов ---------------------------------------------------------
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/targets"
render "$SRC/site/telegram-gateway.http.conf.template"   > "$STAGE/telegram-gateway.http.conf"
render "$SRC/site/telegram-gateway.stream.conf.template" > "$STAGE/telegram-gateway.stream.conf"
cp "$SRC/targets/vps.http.conf" "$SRC/targets/vps.stream.conf" "$STAGE/targets/"
render "$SRC/targets/home.http.conf.template"   > "$STAGE/targets/home.http.conf"
render "$SRC/targets/home.stream.conf.template" > "$STAGE/targets/home.stream.conf"
tar -C "$STAGE" -czf - . | vps_ssh "rm -rf /tmp/tg-nginx && mkdir -p /tmp/tg-nginx && tar -C /tmp/tg-nginx -xzf -"

# --- 2-4 на VPS --------------------------------------------------------------
vps_ssh "DOMAIN='$DOMAIN' ACME_EMAIL='$ACME_EMAIL' NAMES='${NAMES[*]}' bash -s" <<'REMOTE'
set -euo pipefail
S=/tmp/tg-nginx

echo "== пакеты"
need=()
command -v nginx   >/dev/null || need+=(nginx)
command -v certbot >/dev/null || need+=(certbot)
dpkg -s libnginx-mod-stream >/dev/null 2>&1 || need+=(libnginx-mod-stream)
if [ "${#need[@]}" -gt 0 ]; then
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${need[@]}"
fi

echo "== stream {} в nginx.conf"
mkdir -p /etc/nginx/streams-enabled /etc/nginx/webtgk-targets /var/www/html
if ! grep -q 'streams-enabled' /etc/nginx/nginx.conf; then
  cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak.$(date +%Y%m%d-%H%M%S)
  printf '\nstream {\n\tinclude /etc/nginx/streams-enabled/*.conf;\n}\n' >> /etc/nginx/nginx.conf
fi

echo "== сертификат Let's Encrypt"
CERT=/etc/letsencrypt/live/$DOMAIN/fullchain.pem
have_all=1
if [ -f "$CERT" ]; then
  sans=$(openssl x509 -in "$CERT" -noout -ext subjectAltName 2>/dev/null)
  for n in $NAMES; do grep -q "DNS:$n\b" <<<"$sans" || have_all=0; done
else
  have_all=0
fi
if [ "$have_all" = 0 ]; then
  if [ ! -f /etc/nginx/sites-enabled/telegram-gateway ]; then
    # Временный :80 только для ACME (полный конфиг требует уже выпущенный сертификат).
    cat > /etc/nginx/sites-enabled/telegram-gateway <<EOF
server {
    listen 80;
    server_name $NAMES;
    location /.well-known/acme-challenge/ { root /var/www/html; }
}
EOF
  fi
  nginx -t && systemctl reload nginx
  args=(); for n in $NAMES; do args+=(-d "$n"); done
  certbot certonly --webroot -w /var/www/html --cert-name "$DOMAIN" --expand \
    "${args[@]}" -m "$ACME_EMAIL" --agree-tos -n \
    --deploy-hook "systemctl reload nginx"
else
  echo "  уже есть: $CERT"
fi

echo "== конфиги"
TS=$(date +%Y%m%d-%H%M%S)
for f in /etc/nginx/sites-enabled/telegram-gateway /etc/nginx/streams-enabled/telegram-gateway.conf; do
  [ -e "$f" ] && cp "$f" "/root/$(basename "$f").bak.$TS" || true
done
cp "$S"/targets/*.conf /etc/nginx/webtgk-targets/
[ -L /etc/nginx/conf.d/zz-webtgk-target.conf ] || \
  ln -sfn /etc/nginx/webtgk-targets/vps.http.conf /etc/nginx/conf.d/zz-webtgk-target.conf
[ -L /etc/nginx/streams-enabled/zz-webtgk-target.conf ] || \
  ln -sfn /etc/nginx/webtgk-targets/vps.stream.conf /etc/nginx/streams-enabled/zz-webtgk-target.conf
cp "$S/telegram-gateway.http.conf"   /etc/nginx/sites-enabled/telegram-gateway
cp "$S/telegram-gateway.stream.conf" /etc/nginx/streams-enabled/telegram-gateway.conf
if nginx -t; then
  systemctl reload nginx
  echo "nginx готов, таргет: $(readlink /etc/nginx/conf.d/zz-webtgk-target.conf)"
else
  for f in /etc/nginx/sites-enabled/telegram-gateway /etc/nginx/streams-enabled/telegram-gateway.conf; do
    b="/root/$(basename "$f").bak.$TS"; if [ -e "$b" ]; then cp "$b" "$f"; else rm -f "$f"; fi
  done
  echo "nginx -t не прошёл — конфиги откатены" >&2; exit 1
fi
rm -rf "$S"
REMOTE
log "готово: https://$DOMAIN (до деплоя стека — 502)."
