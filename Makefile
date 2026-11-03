SHELL := /bin/bash
.DEFAULT_GOAL := help

# Go-команды выполняются в Docker (golang:1.25-alpine), чтобы не требовать
# установки Go на хост. GOMODCACHE выносится в /tmp внутри контейнера.
GO_DOCKER := docker run --rm -v "$(PWD)/bot:/src" -w /src
GO_IMAGE := golang:1.25-alpine

.PHONY: help up down ps build test lint cert ca clean api-test web-test bot-test bot-build migrate-up migrate-down userctl vps-prepare vps-harden vps-nginx vps-deploy vps-check vps-status vps-logs ingress-status ingress-vps ingress-home move-to-vps move-to-home

help: ## Показать доступные команды
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

# ===== Развёртывание на VPS (README «Развёртывание на VPS», docs/deploy/) =====
# Все настройки — в .env.vps (шаблон .env.vps.example).
vps-prepare: ## VPS: подготовка хоста (Docker, swap, журнал, службы); ARGS=--dry-run
	@./scripts/vps/prepare-host.sh $(ARGS)

vps-harden: ## VPS: защита (security-обновления, sshd, ufw, sysctl); ARGS=--dry-run
	@./scripts/vps/harden.sh $(ARGS)

vps-nginx: ## VPS: nginx + Let's Encrypt для DOMAIN (чистый VPS); ARGS=--check
	@./scripts/vps/setup-nginx.sh $(ARGS)

vps-deploy: ## VPS: собрать образы здесь, отправить на VPS, поднять стек; ARGS=--no-build
	@./scripts/vps/deploy.sh $(ARGS)

vps-check: ## VPS: /health, выход в Telegram, память
	@./scripts/vps/deploy.sh --check

vps-status: ## VPS: docker compose ps
	@./scripts/vps/deploy.sh --status

vps-logs: ## VPS: логи стека; ARGS=<сервис>
	@./scripts/vps/deploy.sh --logs $(ARGS)

ingress-status: ## VPS nginx: куда сейчас идёт трафик шлюза (vps/home)
	@./scripts/vps/switch-ingress.sh status

ingress-vps: ## VPS nginx: трафик -> стек на VPS
	@./scripts/vps/switch-ingress.sh vps

ingress-home: ## VPS nginx: трафик -> домашний стек через frp
	@./scripts/vps/switch-ingress.sh home

move-to-vps: ## Перенести прод дом -> VPS (БД + переключение трафика)
	@./scripts/vps/move.sh to-vps

move-to-home: ## Перенести прод VPS -> дом (БД + переключение трафика)
	@./scripts/vps/move.sh to-home

up: cert ## Поднять все сервисы (compose)
	@started="$$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"; \
	  docker compose up -d --build && \
	  for i in $$(seq 1 30); do \
	    curl -fsS http://127.0.0.1:8080/health >/dev/null 2>&1 && break; \
	    if [ "$$i" -eq 30 ]; then echo 'api /health не отвечает' >&2; exit 1; fi; \
	    sleep 2; \
	  done; \
	  docker compose logs --since "$$started" --no-color api | sed -n '/BOOTSTRAP_ADMIN_\(EMAIL\|PASSWORD\)=/p'

down: ## Остановить и удалить контейнеры
	docker compose down

build: ## Собрать образы без запуска
	@docker compose build

test: ## Запуск всех тестов (api + web + bot)
	$(MAKE) test-api
	$(MAKE) test-web
	$(MAKE) bot-test

test-api: ## Тесты Go api (в docker)
	@docker run --rm -v "$(PWD)/api:/src" -w /src $(GO_IMAGE) sh -c "go mod download && go test ./..."

test-web: ## Тесты фронтенда
	cd web && npm run test

bot-test: ## Тесты бота (в docker)
	@docker run --rm -v "$(PWD)/bot:/src" -w /src $(GO_IMAGE) sh -c "go mod download && go test ./..."

bot-vet: ## Статический анализ бота (в docker)
	@$(GO_DOCKER) $(GO_IMAGE) go vet ./...

bot-fmt: ## Проверка форматирования бота (в docker)
	@$(GO_DOCKER) $(GO_IMAGE) sh -c "test -z \"\$$(gofmt -l .)\""

bot-build: ## Собрать образ бота
	@docker compose build bot

lint: ## Линт api (golangci-lint) и web (eslint)
	@docker run --rm -v "$(PWD)/api:/app" -w /app golangci/golangci-lint:v2.12.2-alpine golangci-lint run
	cd web && npm run lint
	@docker run --rm -v "$(PWD)/bot:/app" -w /app golangci/golangci-lint:v2.12.2-alpine golangci-lint run

# Локальные миграции для БД на 127.0.0.1:5432.
# MIGRATE_EXPECT_DB обязателен — гард в бинаре не даст мигрировать чужую БД.
PROD_DSN ?= postgres://tg:$(shell grep -o '^POSTGRES_PASSWORD=.*' .env 2>/dev/null | cut -d= -f2 | head -1 || echo tg_dev_password)@127.0.0.1:5432/telegram_gateway

migrate-up: ## Применить миграции прод (требует запущенный прод postgres на :5432)
	@docker run --rm --network host -e RUN_MIGRATIONS=up -e MIGRATE_EXPECT_DB=telegram_gateway -e POSTGRES_DSN="$(POSTGRES_DSN:=$(PROD_DSN))" -v "$(PWD)/api:/src" -w /src $(GO_IMAGE) sh -c "go mod download && go run ./cmd/api"

migrate-down: ## Откатить миграции прод (требует запущенный прод postgres на :5432)
	@docker run --rm --network host -e RUN_MIGRATIONS=down -e MIGRATE_EXPECT_DB=telegram_gateway -e POSTGRES_DSN="$(POSTGRES_DSN:=$(PROD_DSN))" -v "$(PWD)/api:/src" -w /src $(GO_IMAGE) sh -c "go mod download && go run ./cmd/api"

CERT_DIR := infra/nginx/certs
# Держим серверный сертификат валидным минимум на 7 дней.
SERVER_CHECK_DAYS := 604800
# SAN для серверного сертификата. По умолчанию берёт IP шлюза из .env
# (DC_REWRITE_IP) + localhost для локальной разработки. Переопределяется
# переменной CA_SAN=...
CA_SAN ?= DNS:localhost,IP:127.0.0.1

cert: ## Сгенерировать TLS-сертификаты (самоподписанные, либо через внутренний CA, если он созда)
	@mkdir -p $(CERT_DIR)
	@if [ -f $(CERT_DIR)/server.crt ] && openssl x509 -in $(CERT_DIR)/server.crt -noout -checkend $(SERVER_CHECK_DAYS) >/dev/null 2>&1; then \
		echo "Server certificate is still valid, skipping."; \
	elif [ -f $(CERT_DIR)/ca.key ] && [ -f $(CERT_DIR)/ca.crt ]; then \
		echo "Signing server certificate with the internal CA (SAN: $(CA_SAN))..."; \
		openssl req -new -newkey rsa:2048 -nodes \
			-keyout $(CERT_DIR)/server.key \
			-out /tmp/tg-gateway-server.csr \
			-subj "/CN=gateway.lan"; \
		openssl x509 -req -in /tmp/tg-gateway-server.csr \
			-CA $(CERT_DIR)/ca.crt -CAkey $(CERT_DIR)/ca.key -CAcreateserial \
			-out $(CERT_DIR)/server.crt -days 825 -sha256 \
			-extfile <(printf "subjectAltName=$(CA_SAN)\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n"); \
		rm -f /tmp/tg-gateway-server.csr; \
		echo "OK: server.crt signed by the internal CA (subject: $$(openssl x509 -in $(CERT_DIR)/ca.crt -noout -subject))"; \
	else \
		echo "No internal CA found, generating self-signed certificate (CN=localhost)..."; \
		openssl req -x509 -newkey rsa:2048 -nodes \
			-keyout $(CERT_DIR)/server.key \
			-out $(CERT_DIR)/server.crt \
			-days 365 -subj "/CN=localhost"; \
	fi

ca: ## Создать внутренний CA и подписать им серверный сертификат
	@mkdir -p $(CERT_DIR)
	@if [ ! -f $(CERT_DIR)/ca.key ] || [ ! -f $(CERT_DIR)/ca.crt ]; then \
		echo "Generating internal CA (Telegram Gateway Internal CA)..."; \
		openssl req -x509 -newkey rsa:4096 -nodes -sha256 -days 3650 \
			-keyout $(CERT_DIR)/ca.key \
			-out $(CERT_DIR)/ca.crt \
			-subj "/CN=Telegram Gateway Internal CA"; \
	else \
		echo "Internal CA already exists."; \
	fi
	@rm -f $(CERT_DIR)/server.crt $(CERT_DIR)/server.key
	@$(MAKE) cert

clean: ## Удалить образы и volumes
	docker compose down -v --remove-orphans

userctl: ## Создать/обновить пользователя (справка: ./scripts/userctl.sh --help)
	@./scripts/userctl.sh --help
