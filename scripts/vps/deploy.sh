#!/usr/bin/env bash
# Деплой/обновление прод-стека на VPS (docs/deploy/VPS.md §6).
# Запускается на машине с Docker и этим репозиторием (не на VPS). Образы
# собираются ЗДЕСЬ — сборка Go и web (npm) на 1 ГБ ОЗУ упирается в OOM — и
# доставляются на VPS через `docker save | ssh docker load` (только
# изменившиеся). VPS не нужен доступ к Docker Hub.
#
# Шаги: гарды -> build -> отправка образов -> рендер xray-конфига -> загрузка
# compose/nginx/.env -> up (migrate с MIGRATE_EXPECT_DB) -> проверки.
#
# Usage: deploy.sh [--no-build|--check|--status|--logs [service]]
set -euo pipefail
source "$(dirname "$0")/lib.sh"

FLAG="${1:-}"

check() {
  local code
  vps_ssh "curl -sf -m 5 http://127.0.0.1:18090/health" >/dev/null \
    && log "api /health: OK" || { log "api /health: FAIL"; return 1; }
  if profile_enabled xray; then
    code="$(vps_ssh "curl -s -m 20 -o /dev/null -w '%{http_code}' --socks5-hostname 127.0.0.1:10808 https://web.telegram.org/" || true)"
    via="через xray"
  else
    code="$(vps_ssh "curl -s -m 20 -o /dev/null -w '%{http_code}' https://web.telegram.org/" || true)"
    via="напрямую"
  fi
  case "$code" in
    2??|3??) log "web.telegram.org $via: HTTP $code — OK" ;;
    *) log "web.telegram.org $via: '$code' — FAIL (docs/deploy/VPS.md «Диагностика»)"; return 1 ;;
  esac
  vps_ssh "free -m; docker stats --no-stream --format 'table {{.Name}}\t{{.MemUsage}}\t{{.CPUPerc}}'"
}

case "$FLAG" in
  --status) vps_compose ps; exit 0 ;;
  --logs)   vps_compose logs -f --tail=200 "${2:-}"; exit 0 ;;
  --check)  check; exit 0 ;;
  ""|--no-build) ;;
  *) die "неизвестный флаг: $FLAG (--no-build|--check|--status|--logs)" ;;
esac

# --- гарды -------------------------------------------------------------------
[ -f "$PROD_DIR/docker-compose.yml" ] || die "$PROD_DIR: нет docker-compose.yml."
require_clean_tree
if grep -nE '^[A-Z_]+=.*<[^>]*>' "$VPS_ENV" >&2; then
  die "в $VPS_ENV остались незаполненные <...> (строки выше)."
fi
for key in POSTGRES_PASSWORD BOT_API_TOKEN GATEWAY_URL DC_REWRITE_DOMAIN; do
  [ -n "$(env_get "$VPS_ENV" "$key")" ] || die "$key пуст в $VPS_ENV."
done
for weak in tg_dev_password dev_bot_api_token change_me_strong_pass; do
  if grep -qE "=${weak}\$" "$VPS_ENV"; then
    die "в $VPS_ENV пароль/токен из примера ($weak) — сгенерируйте: openssl rand -hex 24."
  fi
done
grep -q 'docker-compose.vps.yml' <<<"$(env_get "$VPS_ENV" COMPOSE_FILE)" \
  || die "COMPOSE_FILE в $VPS_ENV должен включать docker-compose.vps.yml."
if profile_enabled xray; then
  [ -n "$(env_get "$VPS_ENV" XRAY_LINK_1)" ] || die "XRAY_LINK_1 пуст в $VPS_ENV (профиль xray включён)."
  [ "$(env_get "$VPS_ENV" SOCKS5_ADDR)" = "xray:10808" ] \
    || die "при профиле xray SOCKS5_ADDR должен быть xray:10808."
elif grep -qE '^(SOCKS5_ADDR|BOT_HTTPS_PROXY)=.*xray' "$VPS_ENV"; then
  die "SOCKS5_ADDR/BOT_HTTPS_PROXY указывают на xray, но профиль xray выключен (COMPOSE_PROFILES)."
fi

COMMIT="$(git -C "$PROD_DIR" rev-parse --short HEAD)"
log "деплой commit $COMMIT -> $VPS_HOST:$VPS_DIR"
vps_ssh "test -d '$VPS_DIR'" || die "на VPS нет $VPS_DIR — сначала scripts/vps/prepare-host.sh."

# --- сборка ------------------------------------------------------------------
if [ "$FLAG" != "--no-build" ]; then
  log "build ${BUILD_SERVICES[*]} ..."
  # С VPS-профилем: образы telegram-gateway-vps-*, домашние prod-теги не трогаем.
  docker compose -f "$PROD_DIR/docker-compose.yml" -f "$PROD_DIR/docker-compose.vps.yml" \
    --env-file "$VPS_ENV" build "${BUILD_SERVICES[@]}"
fi

# --- образы ------------------------------------------------------------------
mapfile -t IMAGES < <(docker compose -f "$PROD_DIR/docker-compose.yml" -f "$PROD_DIR/docker-compose.vps.yml" \
  --env-file "$VPS_ENV" config --images | sort -u)
TO_SHIP=()
for img in "${IMAGES[@]}"; do
  docker image inspect "$img" >/dev/null 2>&1 || { log "pull $img"; docker pull -q "$img" >/dev/null; }
  local_id="$(docker image inspect -f '{{.Id}}' "$img")"
  remote_id="$(vps_ssh "docker image inspect -f '{{.Id}}' '$img' 2>/dev/null" || true)"
  if [ "$local_id" = "$remote_id" ]; then
    log "образ $img: актуален"
  else
    TO_SHIP+=("$img")
  fi
done
if [ "${#TO_SHIP[@]}" -gt 0 ]; then
  log "отправка образов: ${TO_SHIP[*]}"
  docker save "${TO_SHIP[@]}" | gzip -1 | vps_ssh "gunzip | docker load"
fi

# --- конфиги -----------------------------------------------------------------
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/infra/nginx/certs" "$STAGE/infra/xray"
cp "$PROD_DIR/docker-compose.yml" "$PROD_DIR/docker-compose.vps.yml" "$STAGE/"
cp -r "$PROD_DIR/infra/nginx/nginx.conf" "$PROD_DIR/infra/nginx/templates" "$STAGE/infra/nginx/"
# Сертификат внутреннего TLS-хопа (nginx VPS -> nginx шлюза): самоподписанный.
if [ ! -f "$PROD_DIR/infra/nginx/certs/server.crt" ]; then
  log "нет infra/nginx/certs/server.crt — генерирую (make cert)"
  make -C "$PROD_DIR" cert >/dev/null
fi
cp "$PROD_DIR/infra/nginx/certs/server.crt" "$PROD_DIR/infra/nginx/certs/server.key" "$STAGE/infra/nginx/certs/"
# Конфиги мониторинга — чтобы COMPOSE_PROFILES=monitoring работал без доп. шагов.
cp -r "$PROD_DIR/infra/prometheus" "$PROD_DIR/infra/grafana" "$PROD_DIR/infra/loki" "$PROD_DIR/infra/promtail" "$STAGE/infra/"
cp "$VPS_ENV" "$STAGE/.env"
echo "$COMMIT" > "$STAGE/REVISION"

if profile_enabled xray; then
  # Конфиг из ссылок vless:// (XRAY_LINK_1..N): >1 ссылки — балансировщик.
  python3 "$PROD_DIR/scripts/vps/xray-config.py" "$VPS_ENV" > "$STAGE/infra/xray/config.json" \
    || die "не удалось собрать конфиг xray из XRAY_LINK_N."
  xray_image="$(docker compose -f "$PROD_DIR/docker-compose.yml" -f "$PROD_DIR/docker-compose.vps.yml" \
    --env-file "$VPS_ENV" config --format json | jq -r '.services.xray.image')"
  docker run --rm -v "$STAGE/infra/xray/config.json:/etc/xray/config.json:ro" "$xray_image" \
    xray -test -config /etc/xray/config.json >/dev/null || die "xray -test не прошёл."
else
  echo '{}' > "$STAGE/infra/xray/config.json"
fi

log "загрузка конфигов в $VPS_DIR ..."
tar -C "$STAGE" -czf - . | vps_ssh "tar -C '$VPS_DIR' -xzf - && chmod 600 '$VPS_DIR/.env' '$VPS_DIR/infra/xray/config.json' '$VPS_DIR/infra/nginx/certs/server.key'"

# --- запуск ------------------------------------------------------------------
log "up -d (migrate с MIGRATE_EXPECT_DB) ..."
BOOTSTRAP_LOG_SINCE="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
vps_compose config --quiet
APP=(migrate api telegram-proxy bot web nginx)
profile_enabled xray && APP=(xray "${APP[@]}")
vps_compose up -d --remove-orphans --force-recreate "${APP[@]}"
# Остальное (postgres/redis, мониторинг при его профиле) — без пересоздания.
vps_compose up -d

WAIT_TRIES=45 wait_for "api /health" vps_ssh "curl -sf -m 5 http://127.0.0.1:18090/health"
vps_compose logs --since "$BOOTSTRAP_LOG_SINCE" --no-color api | sed -n '/BOOTSTRAP_ADMIN_\(EMAIL\|PASSWORD\)=/p'

# Чистая установка: без бэкенда в proxy_pool Telegram Web не откроется
# (README, «Доступ к Telegram Web через SOCKS5»). Только если пул пуст.
vps_ssh "cd '$VPS_DIR' && docker compose exec -T postgres sh -c 'psql -q -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\"'" <<'SQL'
INSERT INTO proxy_pool (url, status, priority)
SELECT 'https://web.telegram.org', 'active', 1
WHERE NOT EXISTS (SELECT 1 FROM proxy_pool);
SQL
check
vps_compose ps
log "задеплоено (commit $COMMIT)."
