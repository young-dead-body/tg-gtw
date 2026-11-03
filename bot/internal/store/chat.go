// Package store persists the bot's chat-id registry in Redis.
package store

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const chatKeyPrefix = "bot:chat:"

// defaultChatTTL is how long a registered chat binding stays valid; the bot
// refreshes it on every /start or /credentials.
const defaultChatTTL = 365 * 24 * time.Hour

// ChatStore maps Telegram usernames to chat ids so notifications (ТЗ §9) can
// be delivered even when the user is not currently writing to the bot.
type ChatStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewChatStore creates a store persisting bindings for ttl (defaults to one
// year when ttl <= 0).
func NewChatStore(rdb *redis.Client, ttl time.Duration) *ChatStore {
	if ttl <= 0 {
		ttl = defaultChatTTL
	}
	return &ChatStore{rdb: rdb, ttl: ttl}
}

// Save binds a normalized Telegram username (without '@') to a chat id.
func (s *ChatStore) Save(ctx context.Context, username string, chatID int64) error {
	return s.rdb.Set(ctx, chatKeyPrefix+username, strconv.FormatInt(chatID, 10), s.ttl).Err()
}

// Get returns the chat id bound to the username.
func (s *ChatStore) Get(ctx context.Context, username string) (int64, bool, error) {
	raw, err := s.rdb.Get(ctx, chatKeyPrefix+username).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
