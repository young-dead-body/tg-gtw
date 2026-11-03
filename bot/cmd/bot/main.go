// Бот уведомлений «Защищённого шлюза доступа к Telegram Web» (S11, ТЗ §9).
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"

	"github.com/company/telegram-gateway/bot/internal/api"
	"github.com/company/telegram-gateway/bot/internal/app"
	"github.com/company/telegram-gateway/bot/internal/events"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Feature toggle (S11): when the tg-bot stack is disabled on the host,
	// exit cleanly instead of failing hard so restart: unless-stopped does not
	// loop. The token requirement still applies when the feature is on.
	botEnabled := parseBoolEnv("TG_BOT_ENABLED", true)
	if !botEnabled {
		log.Println("bot disabled (TG_BOT_ENABLED=false); exiting")
		return
	}

	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if telegramToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	apiToken := os.Getenv("API_TOKEN")
	if apiToken == "" {
		log.Fatal("API_TOKEN is required")
	}
	apiURL := getenv("API_URL", "http://api:8080")
	redisAddr := getenv("REDIS_ADDR", "redis:6379")
	channel := getenv("EVENTS_CHANNEL", "gateway:events")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	svc := app.NewService(api.NewClient(apiURL, apiToken))

	b, err := tele.NewBot(tele.Settings{
		Token:  telegramToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatalf("telegram bot: %v", err)
	}
	svc.SetSender(botSender{b: b})
	svc.Register(b)

	listener := events.NewListener(rdb, channel, svc)
	go func() {
		if err := listener.Start(ctx); err != nil {
			log.Printf("events listener stopped: %v", err)
			cancel()
		}
	}()

	go b.Start()
	log.Printf("bot started (api=%s channel=%s)", apiURL, channel)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")
	cancel()
	b.Stop()
}

// botSender adapts telebot to the app.Sender interface.
type botSender struct {
	b *tele.Bot
}

// Send implements app.Sender.
func (s botSender) Send(chatID int64, text string) error {
	_, err := s.b.Send(&tele.Chat{ID: chatID}, text)
	return err
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseBoolEnv(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return fallback
}
