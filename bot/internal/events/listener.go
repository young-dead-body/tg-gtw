// Package events consumes the gateway's Redis pub/sub channel (S11).
package events

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Event is the wire format published on the channel by the API (notify pkg).
type Event struct {
	Name   string    `json:"event"`
	UserID string    `json:"user_id"`
	At     time.Time `json:"at"`
}

// Handler processes a single business event.
type Handler interface {
	HandleEvent(ctx context.Context, e Event) error
}

// Listener subscribes to the gateway events channel and dispatches events.
type Listener struct {
	rdb     *redis.Client
	channel string
	handler Handler
}

// NewListener subscribes to channel and dispatches to handler.
func NewListener(rdb *redis.Client, channel string, handler Handler) *Listener {
	return &Listener{rdb: rdb, channel: channel, handler: handler}
}

// Start blocks, consuming events until the context is cancelled.
func (l *Listener) Start(ctx context.Context) error {
	sub := l.rdb.Subscribe(ctx, l.channel)
	defer func() { _ = sub.Close() }()

	if _, err := sub.Receive(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var e Event
			if err := json.Unmarshal([]byte(msg.Payload), &e); err != nil {
				log.Printf("events: bad payload: %v", err)
				continue
			}
			if err := l.handler.HandleEvent(ctx, e); err != nil {
				log.Printf("events: handle %s user=%s: %v", e.Name, e.UserID, err)
			}
		}
	}
}
