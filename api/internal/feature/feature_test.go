package feature

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/company/telegram-gateway/api/internal/db"
)

type fakeStore struct {
	mu     sync.Mutex
	values map[string][]byte
	fail   bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string][]byte{}}
}

func (f *fakeStore) GetSetting(_ context.Context, key string) (db.Setting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return db.Setting{}, errors.New("boom")
	}
	v, ok := f.values[key]
	if !ok {
		return db.Setting{}, pgx.ErrNoRows
	}
	return db.Setting{Key: key, Value: v}, nil
}

func (f *fakeStore) SetSetting(_ context.Context, arg db.SetSettingParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("boom")
	}
	f.values[arg.Key] = arg.Value
	return nil
}

func (f *fakeStore) set(key, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = []byte(raw)
}

func TestEnabledNoSettingUsesEnv(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "false")
	m := New(newFakeStore(), "TG_BOT_ENABLED", true, 0)
	on, err := m.Enabled(context.Background(), TelegramBot)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if on {
		t.Fatal("expected false from env default")
	}
}

func TestEnabledMalformedEnvUsesFallback(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "not-a-bool")
	m := New(newFakeStore(), "TG_BOT_ENABLED", true, 0)
	on, err := m.Enabled(context.Background(), TelegramBot)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !on {
		t.Fatal("expected fallback true")
	}
}

func TestEnabledSettingWinsOverEnv(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "true")
	s := newFakeStore()
	raw, _ := json.Marshal("false")
	s.set(TelegramBot, string(raw))
	m := New(s, "TG_BOT_ENABLED", true, 0)
	on, err := m.Enabled(context.Background(), TelegramBot)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if on {
		t.Fatal("expected false from settings, env said true")
	}
}

func TestEnabledCacheTTL(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "true")
	s := newFakeStore()
	m := New(s, "TG_BOT_ENABLED", true, time.Hour)

	now := time.Now()
	m.nowFn = func() time.Time { return now }

	if on, _ := m.Enabled(context.Background(), TelegramBot); !on {
		t.Fatal("expected true")
	}
	// Flip the backing store; the cache should still serve the old value.
	raw, _ := json.Marshal("false")
	s.set(TelegramBot, string(raw))
	if on, _ := m.Enabled(context.Background(), TelegramBot); !on {
		t.Fatal("cached true expected")
	}
}

func TestEnabledCacheExpires(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "true")
	s := newFakeStore()
	m := New(s, "TG_BOT_ENABLED", true, time.Minute)

	now := time.Now()
	m.nowFn = func() time.Time { return now }
	if on, _ := m.Enabled(context.Background(), TelegramBot); !on {
		t.Fatal("fn load")
	}

	raw, _ := json.Marshal("false")
	s.set(TelegramBot, string(raw))
	now = now.Add(2 * time.Minute)
	if on, _ := m.Enabled(context.Background(), TelegramBot); on {
		t.Fatal("expected reloaded false after TTL")
	}
}

func TestEnabledOnStoreErrorFallsBack(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "false")
	s := newFakeStore()
	s.fail = true
	m := New(s, "TG_BOT_ENABLED", true, 0)
	on, err := m.Enabled(context.Background(), TelegramBot)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if on {
		t.Fatal("expected false from env fallback")
	}
}

func TestSeedOnlyWritesAbsentKeys(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "false")
	s := newFakeStore()
	raw, _ := json.Marshal("true")
	s.set(TelegramBot, string(raw)) // already present
	m := New(s, "TG_BOT_ENABLED", true, 0)

	if err := m.Seed(context.Background(), map[string]bool{TelegramBot: false}); err != nil {
		t.Fatalf("seed err: %v", err)
	}
	got, _ := s.GetSetting(context.Background(), TelegramBot)
	var v string
	_ = json.Unmarshal(got.Value, &v)
	if v != "true" {
		t.Fatalf("existing setting should not be overwritten, got %q", v)
	}
}

func TestSeedWritesAbsentKey(t *testing.T) {
	t.Setenv("TG_BOT_ENABLED", "false")
	s := newFakeStore()
	m := New(s, "TG_BOT_ENABLED", true, 0)

	if err := m.Seed(context.Background(), map[string]bool{TelegramBot: false}); err != nil {
		t.Fatalf("seed err: %v", err)
	}
	got, err := s.GetSetting(context.Background(), TelegramBot)
	if err != nil {
		t.Fatalf("expected row: %v", err)
	}
	var v string
	_ = json.Unmarshal(got.Value, &v)
	if v != strconv.FormatBool(false) {
		t.Fatalf("unexpected stored value %q", v)
	}
}
