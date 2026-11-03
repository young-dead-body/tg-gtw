package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type recordingHandler struct {
	mu   sync.Mutex
	got  []Event
	done chan struct{}
}

func (h *recordingHandler) HandleEvent(_ context.Context, e Event) error {
	h.mu.Lock()
	h.got = append(h.got, e)
	done := h.done
	h.mu.Unlock()
	if done != nil {
		close(done)
	}
	return nil
}

func TestListenerDeliversEvent(t *testing.T) {
	mr := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	handler := &recordingHandler{done: make(chan struct{})}
	l := NewListener(rdb, "gateway:events", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	waitSubscribed(t, rdb, "gateway:events")

	raw, err := json.Marshal(Event{Name: "access.expired", UserID: "user-1", At: time.Now()})
	require.NoError(t, err)
	require.NoError(t, rdb.Publish(ctx, "gateway:events", raw).Err())

	select {
	case <-handler.done:
	case <-time.After(3 * time.Second):
		t.Fatal("event not delivered")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	require.Len(t, handler.got, 1)
	require.Equal(t, "access.expired", handler.got[0].Name)
	require.Equal(t, "user-1", handler.got[0].UserID)
}

func TestListenerSkipsMalformedPayload(t *testing.T) {
	mr := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	handler := &recordingHandler{}
	l := NewListener(rdb, "gateway:events", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	waitSubscribed(t, rdb, "gateway:events")

	require.NoError(t, rdb.Publish(ctx, "gateway:events", "not-json").Err())
	require.NoError(t, rdb.Publish(ctx, "gateway:events", "42").Err())

	time.Sleep(200 * time.Millisecond)
	handler.mu.Lock()
	defer handler.mu.Unlock()
	require.Empty(t, handler.got)
}

func TestListenerStopsOnCancel(t *testing.T) {
	mr := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	l := NewListener(rdb, "gateway:events", &recordingHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not stop")
	}
}

// waitSubscribed blocks until the listener's subscription to channel is
// registered on the (mini)redis server, so publishes are not lost.
func waitSubscribed(t *testing.T, rdb *redis.Client, channel string) {
	t.Helper()
	require.Eventually(t, func() bool {
		subs, err := rdb.PubSubNumSub(context.Background(), channel).Result()
		return err == nil && subs[channel] >= 1
	}, 3*time.Second, 10*time.Millisecond)
}
