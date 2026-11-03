# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

# ---- runtime stage ----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata postgresql17-client restic
WORKDIR /app
COPY --from=build /out/api ./api
# Directory for pg_dump backups (T16.4); mounted as a volume in compose.
# /logs is shared with promtail so structured zap logs reach Loki (T17.4).
RUN mkdir -p /backups /logs && chown -R nobody:nobody /backups /logs && chmod 700 /backups

ENV PORT=8080
ENV BACKUP_DIR=/backups
EXPOSE 8080

USER nobody
ENTRYPOINT ["/app/api"]