#!/usr/bin/env bash
# Restore a local SQL dump into an isolated, disposable PostgreSQL container.
set -euo pipefail
if [[ $# != 1 || ! -s "$1" ]]; then echo "Usage: $0 /path/to/backup.sql" >&2; exit 2; fi
name="tg-gateway-restore-check-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
# No host ports or existing volumes are used.
docker run -d --name "$name" -e POSTGRES_HOST_AUTH_METHOD=trust postgres:17-alpine >/dev/null
ready=0
for _ in {1..60}; do
  if docker exec "$name" pg_isready -U postgres >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[[ "$ready" == 1 ]] || { echo 'Test PostgreSQL did not start' >&2; exit 1; }
docker exec "$name" createdb -U postgres restore_check
docker exec -i "$name" psql -X -U postgres -d restore_check --set ON_ERROR_STOP=on --single-transaction < "$1" >/dev/null
docker exec "$name" psql -X -U postgres -d restore_check --set ON_ERROR_STOP=on -c 'SELECT count(*) FROM users; SELECT count(*) FROM consents; SELECT version, dirty FROM schema_migrations;'
echo 'Backup restored successfully in an isolated database.'
