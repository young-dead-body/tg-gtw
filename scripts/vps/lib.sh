# Общие функции скриптов развёртывания на VPS (docs/deploy/).
# Подключается через `source`, сам по себе не запускается.
#
# Все настройки — в .env.vps в корне репозитория (шаблон .env.vps.example):
#   VPS_HOST     ssh-адрес VPS, например root@203.0.113.10   (обязательно)
#   VPS_SSH_KEY  путь к ssh-ключу (пусто — ssh-agent / ~/.ssh/config)
#   VPS_DIR      каталог стека на VPS (по умолчанию /opt/telegram-gateway)
# Путь к самому env-файлу можно переопределить: VPS_ENV=/path make vps-deploy

PROD_DIR="${PROD_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
VPS_ENV="${VPS_ENV:-$PROD_DIR/.env.vps}"

# Сервисы, которые останавливаются на время переноса БД (move.sh).
STOP_SERVICES=(api telegram-proxy bot)
# Собираемые образы.
BUILD_SERVICES=(api migrate bot web telegram-proxy)

log()  { echo "[$(basename "$0")] $*"; }
die()  { echo "[$(basename "$0")] ERROR: $*" >&2; exit 1; }

# Значение переменной из env-файла (последнее вхождение, без кавычек).
env_get() {
  local file="$1" key="$2"
  grep -E "^${key}=" "$file" 2>/dev/null | tail -1 | cut -d= -f2- | sed -E "s/^['\"]//; s/['\"]$//"
}

[ -f "$VPS_ENV" ] || die "нет $VPS_ENV — скопируйте .env.vps.example в .env.vps и заполните (README, «Развёртывание на VPS»)."
VPS_HOST="${VPS_HOST:-$(env_get "$VPS_ENV" VPS_HOST)}"
VPS_SSH_KEY="${VPS_SSH_KEY:-$(env_get "$VPS_ENV" VPS_SSH_KEY)}"
VPS_SSH_KEY="${VPS_SSH_KEY/#\~/$HOME}"
VPS_DIR="${VPS_DIR:-$(env_get "$VPS_ENV" VPS_DIR)}"
VPS_DIR="${VPS_DIR:-/opt/telegram-gateway}"
[ -n "$VPS_HOST" ] || die "VPS_HOST не задан в $VPS_ENV."

# Одно TCP-подключение на весь прогон (ControlMaster): скрипты делают
# десятки ssh-вызовов, а ufw limit на 22 (harden.sh) режет >6 новых
# подключений за 30 с с одного IP.
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15
          -o ControlMaster=auto -o ControlPersist=60s
          -o "ControlPath=${XDG_RUNTIME_DIR:-/tmp}/tg-vps-ssh-%C")
[ -n "$VPS_SSH_KEY" ] && SSH_OPTS+=(-i "$VPS_SSH_KEY")

vps_ssh() {
  ssh "${SSH_OPTS[@]}" "$VPS_HOST" "$@"
}

# docker compose в каталоге стека на VPS (COMPOSE_FILE/PROFILES — из его .env).
vps_compose() {
  vps_ssh "cd '$VPS_DIR' && docker compose $*"
}

# docker compose домашнего (локального) стека: docker-compose.yml + override.
local_compose() {
  (cd "$PROD_DIR" && docker compose "$@")
}

# Включён ли compose-профиль в .env.vps (COMPOSE_PROFILES=a,b).
profile_enabled() {
  [[ ",$(env_get "$VPS_ENV" COMPOSE_PROFILES)," == *",$1,"* ]]
}

require_clean_tree() {
  git -C "$PROD_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "$PROD_DIR не git-репозиторий."
  if [ -n "$(git -C "$PROD_DIR" status --porcelain)" ]; then
    git -C "$PROD_DIR" status --short >&2
    die "рабочее дерево не чистое — закоммитьте изменения перед деплоем."
  fi
}

# Ждёт, пока команда не вернёт 0 (по умолчанию 30 попыток с паузой 2 с).
wait_for() {
  local what="$1"; shift
  local i
  for i in $(seq 1 "${WAIT_TRIES:-30}"); do
    if "$@" >/dev/null 2>&1; then log "$what: OK"; return 0; fi
    sleep 2
  done
  die "$what: не дождались."
}

# Рендер шаблона: подставляются только перечисленные в нём ${VAR}, значения
# берутся из .env.vps (остальные $ — nginx-переменные — не трогаются).
render() {
  local tpl="$1" vars key
  vars="$(grep -oE '\$\{[A-Z][A-Z0-9_]*\}' "$tpl" | sort -u | tr '\n' ' ')"
  (
    for key in $(grep -oE '\$\{[A-Z][A-Z0-9_]*\}' "$tpl" | tr -d '${}' | sort -u); do
      [ -n "${!key:-}" ] || export "$key=$(env_get "$VPS_ENV" "$key")"
    done
    envsubst "$vars" < "$tpl"
  )
}
