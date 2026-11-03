#!/usr/bin/env bash
# Перенос прод-шлюза между домашним сервером (эта машина, стек
# docker-compose.yml + override, доступ через frp) и VPS — вместе с БД
# (docs/deploy/HOME.md). Простой — пока идёт дамп/восстановление (минуты):
# api/telegram-proxy/bot на источнике останавливаются, чтобы в БД не было
# записи после снятия дампа и бот не работал на двух хостах сразу.
#
#   to-vps:  стоп на доме -> pg_dump -> pg_restore на VPS -> старт на VPS
#            -> switch-ingress vps
#   to-home: стоп на VPS -> pg_dump -> pg_restore дома -> старт дома
#            -> switch-ingress home
#
# БД источника не удаляется: откат = перенос в обратную сторону (или restore
# из дампа .data/move/*.dump). Redis (сессии) не переносится — пользователи
# один раз перелогинятся.
#
# Usage: move.sh to-vps|to-home [--yes]
set -euo pipefail
source "$(dirname "$0")/lib.sh"

DIRECTION="${1:-}"
[ "${2:-}" = "--yes" ] && ASSUME_YES=1 || ASSUME_YES=0
START_SERVICES=(api telegram-proxy bot web nginx)

case "$DIRECTION" in
  to-vps|to-home) ;;
  *) die "usage: move.sh to-vps|to-home [--yes]" ;;
esac

[ -f "$PROD_DIR/.env" ] || die "нет $PROD_DIR/.env (env домашнего стека)."

LOCAL_PG_USER="$(env_get "$PROD_DIR/.env" POSTGRES_USER)";  LOCAL_PG_USER="${LOCAL_PG_USER:-tg}"
LOCAL_PG_DB="$(env_get "$PROD_DIR/.env" POSTGRES_DB)";      LOCAL_PG_DB="${LOCAL_PG_DB:-telegram_gateway}"
VPS_PG_USER="$(env_get "$VPS_ENV" POSTGRES_USER)";          VPS_PG_USER="${VPS_PG_USER:-tg}"
VPS_PG_DB="$(env_get "$VPS_ENV" POSTGRES_DB)";              VPS_PG_DB="${VPS_PG_DB:-telegram_gateway}"

DUMP_DIR="$PROD_DIR/.data/move"
mkdir -p "$DUMP_DIR" && chmod 700 "$DUMP_DIR"
DUMP="$DUMP_DIR/$(date +%Y%m%d-%H%M%S)-$DIRECTION.dump"

# Автооткат: если после остановки источника что-то упало (дамп, restore,
# миграции на приёмнике), источник запускается обратно теми же контейнерами
# (start, без пересоздания), трафик не переключался.
SOURCE_STOPPED=""
rollback() {
  local rc=$?
  [ "$rc" = 0 ] || [ -z "$SOURCE_STOPPED" ] && return
  log "ОШИБКА (код $rc) — откат: запускаю ${STOP_SERVICES[*]} на источнике ($SOURCE_STOPPED)"
  if [ "$SOURCE_STOPPED" = home ]; then local_compose start "${STOP_SERVICES[@]}" || true
  else vps_compose start "${STOP_SERVICES[@]}" || true; fi
  log "откат выполнен; трафик не переключался. Дамп сохранён в $DUMP."
}
trap rollback EXIT

confirm() {
  [ "$ASSUME_YES" = 1 ] && return 0
  read -r -p "$1 [y/N] " ans
  [[ "$ans" =~ ^[YyДд]$ ]] || die "отменено."
}

if [ "$DIRECTION" = to-vps ]; then
  # Стек на VPS должен быть задеплоен (scripts/vps/deploy.sh) и выходить в Telegram.
  "$(dirname "$0")/deploy.sh" --check >/dev/null || die "стек на VPS не готов: scripts/vps/deploy.sh --check."
  confirm "Перенести прод дом -> VPS? Простой на время переноса БД."

  log "стоп ${STOP_SERVICES[*]} дома и на VPS ..."
  SOURCE_STOPPED=home
  local_compose stop "${STOP_SERVICES[@]}"
  vps_compose stop "${STOP_SERVICES[@]}"

  log "pg_dump дом -> $DUMP"
  local_compose exec -T postgres pg_dump -U "$LOCAL_PG_USER" -d "$LOCAL_PG_DB" -Fc > "$DUMP"
  chmod 600 "$DUMP"
  [ -s "$DUMP" ] || die "пустой дамп — перенос прерван; вернуть дом: docker compose up -d ${STOP_SERVICES[*]}."

  log "pg_restore на VPS (одна транзакция: при ошибке БД VPS не меняется) ..."
  vps_ssh "cd '$VPS_DIR' && docker compose exec -T postgres pg_restore -U '$VPS_PG_USER' -d '$VPS_PG_DB' --clean --if-exists --no-owner --single-transaction" < "$DUMP"

  log "старт на VPS ..."
  vps_compose up -d "${START_SERVICES[@]}"
  wait_for "VPS api /health" vps_ssh "curl -sf -m 5 http://127.0.0.1:18090/health"

  "$(dirname "$0")/switch-ingress.sh" vps
  SOURCE_STOPPED=""
  log "готово: прод на VPS. Дома остановлены ${STOP_SERVICES[*]}, postgres оставлен для отката."
  log "откат: scripts/vps/move.sh to-home"
else
  confirm "Перенести прод VPS -> дом? Простой на время переноса БД."

  log "старт postgres/redis дома ..."
  local_compose up -d postgres redis
  wait_for "домашний postgres" local_compose exec -T postgres pg_isready -U "$LOCAL_PG_USER" -d "$LOCAL_PG_DB"

  log "стоп ${STOP_SERVICES[*]} на VPS и дома ..."
  SOURCE_STOPPED=vps
  vps_compose stop "${STOP_SERVICES[@]}"
  local_compose stop "${STOP_SERVICES[@]}"

  log "pg_dump VPS -> $DUMP"
  vps_ssh "cd '$VPS_DIR' && docker compose exec -T postgres pg_dump -U '$VPS_PG_USER' -d '$VPS_PG_DB' -Fc" > "$DUMP"
  chmod 600 "$DUMP"
  [ -s "$DUMP" ] || die "пустой дамп — перенос прерван, VPS остановлен: vps_compose up -d для возврата."

  log "pg_restore дома ..."
  local_compose exec -T postgres pg_restore -U "$LOCAL_PG_USER" -d "$LOCAL_PG_DB" --clean --if-exists --no-owner --single-transaction < "$DUMP"

  log "старт дома ..."
  local_compose up -d "${START_SERVICES[@]}"
  wait_for "домашний api /health" curl -sf -m 5 http://127.0.0.1:8080/health

  "$(dirname "$0")/switch-ingress.sh" home
  SOURCE_STOPPED=""
  log "готово: прод дома. На VPS остановлены ${STOP_SERVICES[*]}."
  log "освободить ОЗУ VPS полностью: ssh VPS 'cd $VPS_DIR && docker compose stop'"
fi
