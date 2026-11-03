package app

import (
	"context"
	"log"
)

// Notifier sends out-of-band notifications to the Telegram bot (S11) via the
// shared Redis pub/sub channel. NoopNotifier keeps the contract for tests and
// for deployments where the bot is not running.
type Notifier interface {
	// Notify delivers a business event identified by name with a string payload.
	Notify(ctx context.Context, event string, payload string) error
}

// NoopNotifier logs events without delivering them anywhere.
type NoopNotifier struct{}

// NewNoopNotifier returns a Notifier that only logs.
func NewNoopNotifier() Notifier {
	return &NoopNotifier{}
}

func (n *NoopNotifier) Notify(_ context.Context, event string, payload string) error {
	log.Printf("notifier: event=%s payload=%s", event, payload)
	return nil
}
