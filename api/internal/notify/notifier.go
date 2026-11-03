// Package notify publishes business events to a shared Redis channel consumed
// by the Telegram notification bot (S11, ТЗ §9).
package notify

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Channel is the pub/sub channel carrying gateway business events.
const Channel = "gateway:events"

// Event is the wire format published for every notification-worthy action.
type Event struct {
	// Name is the event discriminator, e.g. "password.changed" or
	// "access.expiring".
	Name   string    `json:"event"`
	UserID string    `json:"user_id"`
	At     time.Time `json:"at"`
}

// Notifier publishes events to the Redis channel. It implements both
// app.Notifier and cron.Notifier by exposing Notify(ctx, event, payload).
type Notifier struct {
	client *redis.Client
	chan_  string
}

// New creates a notifier publishing to Channel.
func New(client *redis.Client) *Notifier {
	return &Notifier{client: client, chan_: Channel}
}

// WithChannel returns a copy publishing to the given channel (for tests).
func (n *Notifier) WithChannel(ch string) *Notifier {
	return &Notifier{client: n.client, chan_: ch}
}

// Notify encodes a business event with a user-id payload and publishes it.
func (n *Notifier) Notify(ctx context.Context, event string, payload string) error {
	e := Event{Name: event, UserID: payload, At: time.Now()}
	raw, err := json.Marshal(&e)
	if err != nil {
		return err
	}
	if err := n.client.Publish(ctx, n.chan_, raw).Err(); err != nil {
		return err
	}
	log.Printf("notify: published %s user=%s to %s", event, payload, n.chan_)
	return nil
}

var _ = json.Marshal
