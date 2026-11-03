#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Использование: userctl.sh <email> [options]

Создаёт пользователя, а если он уже есть — обновляет только переданные
поля (роль, пароль и т.д.). Изменения применяются напрямую в БД, минуя API.

Options:
  --role user|admin        роль (при создании по умолчанию: user)
  --password ПАРОЛЬ        создать/сменить пароль (мин. 8 символов,
                           хешируется Argon2id через docker:golang)
  --first ИМЯ --last ФАМИЛИЯ
  --telegram @handle
  --department ОТДЕЛ --position ДОЛЖНОСТЬ
  --active | --inactive    статус is_active
  --unblock                снять blocked_at и сбросить lockout в Redis
  --expires YYYY-MM-DD|off expires_at ("off" очищает)
  --no-lock-clear          не сбрасывать lockout в Redis при смене пароля
  -h|--help                этот текст

Переменные окружения: POSTGRES_USER, POSTGRES_DB, POSTGRES_PASSWORD,
PG_CONTAINER, REDIS_CONTAINER (по умолчанию берутся из .env / docker ps).

Примеры:
  userctl.sh ivan@example.com --role user --password 'Pass1234!'
  userctl.sh admin-0123456789abcdef@example.com --role admin --password 'NewPass123!' --unblock
  userctl.sh petrov@example.com --inactive --expires off
EOF
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"

env_get() {
  local key="$1" fallback="$2" v
  v="$(grep -E "^$key=" "$ROOT/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
  v="${v%\"}"
  v="${v#\"}"
  v="${v%\'}"
  v="${v#\'}"
  [[ -n "$v" ]] && printf '%s' "$v" || printf '%s' "$fallback"
}

POSTGRES_USER="${POSTGRES_USER:-$(env_get POSTGRES_USER tg)}"
POSTGRES_DB="${POSTGRES_DB:-$(env_get POSTGRES_DB telegram_gateway)}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-$(env_get POSTGRES_PASSWORD '')}"
PG_CONTAINER="${PG_CONTAINER:-$(docker compose -f "$ROOT/docker-compose.yml" --project-directory "$ROOT" ps -q postgres 2>/dev/null || true)}"
REDIS_CONTAINER="${REDIS_CONTAINER:-$(docker compose -f "$ROOT/docker-compose.yml" --project-directory "$ROOT" ps -q redis 2>/dev/null || true)}"

email=""
role=""
password=""
first=""
last=""
telegram=""
department=""
position=""
active=""
expires=""
unblock=false
clear_lock=true

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) role="${2:?--role требует значение}"; shift 2 ;;
    --password) password="$2"; shift 2 ;;
    --first) first="$2"; shift 2 ;;
    --last) last="$2"; shift 2 ;;
    --telegram) telegram="$2"; shift 2 ;;
    --department) department="$2"; shift 2 ;;
    --position) position="$2"; shift 2 ;;
    --active) active=true; shift ;;
    --inactive) active=false; shift ;;
    --unblock) unblock=true; shift ;;
    --expires) expires="$2"; shift 2 ;;
    --no-lock-clear) clear_lock=false; shift ;;
    -h|--help) usage; exit 0 ;;
    -*)
      echo "неизвестный флаг: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      [[ -z "$email" ]] && email="$1" || { echo "лишний аргумент: $1" >&2; exit 2; }
      shift ;;
  esac
done

[[ -n "$email" ]] || { usage >&2; exit 2; }
[[ "$email" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]] || { echo "некорректный email: $email" >&2; exit 2; }
[[ -z "$role" || "$role" == user || "$role" == admin ]] || { echo "роль должна быть user или admin" >&2; exit 2; }
[[ -z "$password" || ${#password} -ge 8 ]] || { echo "пароль должен быть не короче 8 символов" >&2; exit 2; }
if [[ -n "$expires" && "$expires" != "off" ]]; then
  expires_norm="$(date -d "$expires" +%F 2>/dev/null || true)"
  [[ -n "$expires_norm" ]] || { echo "некорректная дата expires: $expires" >&2; exit 2; }
  expires="$expires_norm"
fi

if ! docker ps >/dev/null 2>&1; then
  echo "docker недоступен" >&2
  exit 1
fi
[[ -n "$PG_CONTAINER" && "$(docker inspect -f '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null)" == true ]] || {
  echo "контейнер postgres не найден: $PG_CONTAINER (задайте PG_CONTAINER)" >&2
  exit 1
}

psql_() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -qtA "$@"
}

existing="$(psql_ -v email="$email" 2>&1 <<'SQL'
SELECT id FROM users WHERE email=:'email' LIMIT 1
SQL
)"

if [[ -z "$existing" && -z "$password" ]]; then
  echo "Создаётся пользователь без пароля — он не сможет войти." >&2
fi

redis_del() {
  [[ "$clear_lock" == false ]] && return 0
  if [[ -n "$REDIS_CONTAINER" && "$(docker inspect -f '{{.State.Running}}' "$REDIS_CONTAINER" 2>/dev/null)" == true ]]; then
    docker exec "$REDIS_CONTAINER" redis-cli DEL "email:$email" >/dev/null 2>&1 || true
    echo "* lockout в Redis сброшен (email:$email)"
  fi
}

hash_password() {
  docker run --rm -e HASH_IN="$1" -v "$ROOT/api:/src" -w /src golang:1.25-alpine \
    sh -c 'go mod download && go run ./cmd/hashpass "$HASH_IN"'
}

join() {
  local IFS=, s=""
  for x in "$@"; do s="${s:+$s,}$x"; done
  echo "$s"
}

changed=()
args=(-v email="$email")

if [[ -n "$existing" ]]; then
  echo "Пользователь $email найден, обновляю:"
  sets=()
  [[ -n "$role" ]] && { sets+=("role=:'role'"); args+=(-v role="$role"); }
  [[ -n "$active" ]] && { sets+=("is_active=:'active'"); args+=(-v active="$active"); }
  [[ -n "$first" ]] && { sets+=("first_name=:'first'"); args+=(-v first="$first"); }
  [[ -n "$last" ]] && { sets+=("last_name=:'last'"); args+=(-v last="$last"); }
  [[ -n "$telegram" ]] && { sets+=("telegram=:'telegram'"); args+=(-v telegram="$telegram"); }
  [[ -n "$department" ]] && { sets+=("department=:'department'"); args+=(-v department="$department"); }
  [[ -n "$position" ]] && { sets+=("position=:'position'"); args+=(-v position="$position"); }
  if [[ -n "$expires" ]]; then
    if [[ "$expires" == "off" ]]; then
      args+=(-v expires="NULL")
      sets+=("expires_at=:expires")
    else
      args+=(-v expires="$expires")
      sets+=("expires_at=:'expires'::date")
    fi
  fi
  if [[ -n "$password" ]]; then
    hash="$(hash_password "$password")"
    args+=(-v password_hash="$hash")
    sets+=("password_hash=:'password_hash'")
    sets+=("password_changed_at=now()")
    sets+=("updated_at=now()")
  fi
  [[ "$unblock" == true ]] && sets+=("blocked_at=NULL")
  if [[ "${#sets[@]}" -eq 0 ]]; then
    echo "  ничего не изменилось — передайте хотя бы один флаг"
    exit 0
  fi
  sql="UPDATE users SET $(join "${sets[@]}") WHERE email=:'email'"
  printf '%s\n' "$sql" | psql_ "${args[@]}" >/dev/null
  echo "  updated: $(join "${sets[@]}")"
  if [[ -n "$password" ]]; then
    printf '%s\n' \
      "INSERT INTO password_history (user_id, password_hash) SELECT id, :'password_hash' FROM users WHERE email=:'email'" |
      psql_ -v email="$email" -v password_hash="$hash" >/dev/null
    echo "  пароль сменён, запись в password_history добавлена"
  fi
  [[ "$unblock" == true || -n "$password" ]] && redis_del
else
  echo "Создаю пользователя $email:"
  role="${role:-user}"
  cols=("email,role,first_name,last_name")
  vals=(":'email',:'role','',''")
  args+=(-v role="$role")
  [[ -n "$password" ]] && { hash="$(hash_password "$password")"; args+=(-v password_hash="$hash"); cols+=("password_hash"); vals+=(":'password_hash'"); }
  [[ -n "$first" ]] && { cols+=("first_name"); vals+=(":'first'"); args+=(-v first="$first"); }
  [[ -n "$last" ]] && { cols+=("last_name"); vals+=(":'last'"); args+=(-v last="$last"); }
  [[ -n "$telegram" ]] && { cols+=("telegram"); vals+=(":'telegram'"); args+=(-v telegram="$telegram"); }
  [[ -n "$department" ]] && { cols+=("department"); vals+=(":'department'"); args+=(-v department="$department"); }
  [[ -n "$position" ]] && { cols+=("position"); vals+=(":'position'"); args+=(-v position="$position"); }
  [[ -n "$active" ]] && { cols+=("is_active"); vals+=(":'active'"); args+=(-v active="$active"); }
  if [[ -n "$expires" ]]; then
    if [[ "$expires" == "off" ]]; then args+=(-v expires="NULL"); vals+=(":expires");
    else args+=(-v expires="$expires"); vals+=(":'expires'::date"); fi
    cols+=("expires_at")
  fi
  sql="INSERT INTO users ($(join "${cols[@]}")) VALUES ($(join "${vals[@]}")) RETURNING id"
  new_id="$(printf '%s\n' "$sql" | psql_ "${args[@]}")"
  echo "  создан id=$new_id, роль=$role"
  if [[ -n "$password" ]]; then
    printf '%s\n' \
      "INSERT INTO password_history (user_id, password_hash) SELECT id, :'password_hash' FROM users WHERE email=:'email'" |
      psql_ -v email="$email" -v password_hash="$hash" >/dev/null
    echo "  пароль установлен (Argon2id), история добавлена"
  else
    echo "  WARNING: пароль не задан — пользователь не сможет войти, перезапустите с --password" >&2
  fi
fi
