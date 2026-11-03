# Смена пароля администратора

Инструкция по смене пароля админ-учётки шлюза через веб-интерфейс,
CLI-скрипт `userctl.sh` или напрямую в БД.

Первый администратор создаётся автоматически при пустой БД. Логин и пароль
генерируются случайно и выводятся в stdout контейнера API только при создании.
После `make up` или `make vps-deploy` они видны в терминале. Позже их можно
прочитать через `docker compose logs api` (на VPS — `make vps-logs ARGS=api`).
Новый запуск не создаёт и не печатает новые данные, если администратор уже есть.
Журнал контейнера содержит исходный пароль: ограничьте к нему доступ.

В примерах ниже `admin-0123456789abcdef@example.com` — условный логин.
Подставьте выведенный при первом запуске адрес вашего администратора.

---

## Способ 1. Через веб-интерфейс (самый простой)

1. Войдите в админ-панель шлюза.
2. Меню «Пользователи» → найдите админа (или себя).
3. Нажмите «Изменить» → укажите новый пароль → сохраните.

Пароль хэшируется Argon2id, обновляется `password_changed_at` и пишется строка в
`password_history` (учитывается политика ротации и запрет повторного
использования последних 5 паролей).

> Требуется, чтобы вход в систему работал. Если вход заблокирован (lockout) или
> пароль забыт — используйте Способ 2.

---

## Способ 2. Через `userctl.sh` (когда вход недоступен)

Скрипт `scripts/userctl.sh` меняет пароль **напрямую в БД, минуя API**. Он
хэширует пароль Argon2id (через `api/cmd/hashpass` в golang-контейнере),
обновляет `password_hash`, `password_changed_at` и добавляет запись в
`password_history`.

```bash
# смена пароля админа + сброс lockout
./scripts/userctl.sh admin-0123456789abcdef@example.com --role admin --password 'НовыйПароль123!' --unblock
```

Параметры:
- `--password 'ПАРОЛЬ'` — пароль **не короче 8 символов**;
- `--role admin` — удержать роль админа при обновлении;
- `--unblock` — снять `blocked_at` (если аккаунт блокирован) и сбросить
  lockout-счётчик в Redis (ключи `email:<email>` и `ip:*`).

Примеры:
```bash
# просто сменить пароль
./scripts/userctl.sh admin-0123456789abcdef@example.com --role admin --password 'NewPass123!'

# сменить + снять блокировку
./scripts/userctl.sh admin-0123456789abcdef@example.com --role admin --password 'NewPass123!' --unblock

# снять только блокировку, пароль не трогать
./scripts/userctl.sh admin-0123456789abcdef@example.com --unblock
```

Скрипт сам определяет контейнеры Postgres/Redis из `.env` и `docker ps`.
Если они не угадались — задайте явно:

```bash
PG_CONTAINER=telegram-gateway-postgres-1 REDIS_CONTAINER=telegram-gateway-redis-1 \
  ./scripts/userctl.sh admin-0123456789abcdef@example.com --role admin --password 'NewPass123!' --unblock
```

Полная справка: `./scripts/userctl.sh --help`.

---

## Способ 3. Вручную через psql (крайний случай)

Подходит, если `userctl.sh` недоступен. Нужен свежий Argon2id-хэш (солт
случайный — генерируется при каждом вызове):

```bash
# 1) сгенерировать хэш пароля (Argon2id)
docker run --rm -e HASH_IN='НовыйПароль123!' \
  -v "$(pwd)/api:/src" -w /src golang:1.25-alpine \
  sh -c 'go mod download && go run ./cmd/hashpass "$HASH_IN"'

# 2) записать в БД (подставьте выведенный хэш)
docker compose exec postgres psql -U tg -d telegram_gateway -v ON_ERROR_STOP=1 -c "
  UPDATE users
     SET password_hash = '$argon2id$...', password_changed_at = now(), updated_at = now()
   WHERE email = 'admin-0123456789abcdef@example.com';
"

# 3) добавить в историю паролей (необязательно, но соответствует политике)
docker compose exec postgres psql -U tg -d telegram_gateway -v ON_ERROR_STOP=1 -c "
  INSERT INTO password_history (user_id, password_hash)
  SELECT id, '$argon2id$...' FROM users WHERE email = 'admin-0123456789abcdef@example.com';
"
```

Обязательно сбросьте lockout (см. ниже), иначе вход продолжит блокироваться.

---

## Типичные проблемы

### «too many failed attempts, try later» (lockout)

Это блокировка за перебор: **5 неудачных попыток за 15 минут** (ключи `ip:*` и
`email:*`, пороги `LOCKOUT_THRESHOLD` / `LOCKOUT_WINDOW`).

Снять немедленно:
```bash
./scripts/userctl.sh admin-0123456789abcdef@example.com --unblock
```
или напрямую в Redis:
```bash
docker compose exec redis redis-cli DEL "email:admin-0123456789abcdef@example.com"
# и по IP-адресу, если блокировка висит на ip:
docker compose exec redis redis-cli --scan --pattern 'ip:*' | xargs -r docker compose exec redis redis-cli DEL
```
Либо просто подождать окончания окна (по умолчанию 15 минут) — блокировка
снимается автоматически и при успешном входе.

### Пароль первого администратора утерян

Пароль не хранится в `.env` и повторно не генерируется. Найдите его в логе
первого запуска API или задайте новый через `userctl.sh` по логину из БД.

### Пароль забракован (политика ротации/истории)

- Пароль короче 8 символов → отклоняется (min 8).
- Пароль совпадает с одним из последних 5 → отклоняется (история в
  `password_history`).
- Истёк `password_changed_at + RotationPeriod` (по умолчанию 90 дней) → вход
  вернёт `403 password_expired` до смены пароля.

---

## Безопасность

- Хэширование паролей — **Argon2id** (`api/internal/password/password.go`),
  сравнение в constant-time.
- Начальные учётные данные присутствуют в stdout API и журнале контейнера;
  доступ к логам должен быть ограничен. После первого входа смените пароль.
