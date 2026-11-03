// Package feature provides runtime feature toggles backed by the settings
// table, with a host-provided env default as fallback.
package feature

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/company/telegram-gateway/api/internal/db"
)

// TelegramBot is the settings key for the tg-bot feature toggle. Its value is
// stored as a JSON boolean string ("true"/"false") so it can be managed through
// the admin settings API like the other settings rows. It is also exposed via
// the webUI so the telegram field can be hidden when the bot is off.
const TelegramBot = "telegram.bot"

// SettingStore is the subset of db.Querier needed to read and seed a toggle.
type SettingStore interface {
	GetSetting(ctx context.Context, key string) (db.Setting, error)
	SetSetting(ctx context.Context, arg db.SetSettingParams) error
}

// Manager resolves feature toggles. Values are cached for TTL so the hot path
// does not hit the database on every call. A setting present in the table wins
// over the env default, which in turn wins over the built-in default.
type Manager struct {
	store    SettingStore
	envKey   string
	fallback bool
	ttl      time.Duration

	mu    sync.Mutex
	nowFn func() time.Time
	cache map[string]cacheEntry
}

type cacheEntry struct {
	enabled bool
	at      time.Time
}

// New returns a Manager reading toggles from store. envKey names the
// environment variable providing the default when no settings row exists;
// fallback is used when the env var is also unset. Positive TTL enables
// caching; a zero/negative TTL disables it (tests).
func New(store SettingStore, envKey string, fallback bool, ttl time.Duration) *Manager {
	return &Manager{
		store:    store,
		envKey:   envKey,
		fallback: fallback,
		ttl:      ttl,
		nowFn:    time.Now,
		cache:    map[string]cacheEntry{},
	}
}

// Enabled reports whether the named feature is on.
func (m *Manager) Enabled(ctx context.Context, key string) (bool, error) {
	if v, ok := m.cached(key); ok {
		return v, nil
	}

	enabled, err := m.resolve(ctx, key)
	if err != nil {
		// On any read error fall back to env/default so the app keeps working
		// and returns a sane result.
		return m.defaultEnabled(), nil
	}

	m.mu.Lock()
	m.cache[key] = cacheEntry{enabled: enabled, at: m.nowFn()}
	m.mu.Unlock()
	return enabled, nil
}

// EnabledOr returns Enabled with a static fallback for callers that ignore
// errors (e.g. startup wiring).
func (m *Manager) EnabledOr(ctx context.Context, key string, or bool) bool {
	v, err := m.Enabled(ctx, key)
	if err != nil {
		return or
	}
	return v
}

// Seed writes the env default into the settings table for every given key,
// but only when the key is absent. This lets the host value (TG_BOT_ENABLED)
// land in the DB once, after which the admin can override it at runtime.
func (m *Manager) Seed(ctx context.Context, pairs map[string]bool) error {
	for key, want := range pairs {
		if _, err := m.store.GetSetting(ctx, key); err == nil {
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(strconv.FormatBool(want))
		if err != nil {
			return err
		}
		if err := m.store.SetSetting(ctx, db.SetSettingParams{Key: key, Value: raw}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) resolve(ctx context.Context, key string) (bool, error) {
	setting, err := m.store.GetSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return m.defaultEnabled(), nil
	}
	if err != nil {
		return false, err
	}
	var raw string
	if err := json.Unmarshal(setting.Value, &raw); err == nil {
		if b, perr := strconv.ParseBool(raw); perr == nil {
			return b, nil
		}
	}
	return m.defaultEnabled(), nil
}

func (m *Manager) defaultEnabled() bool {
	if s := os.Getenv(m.envKey); s != "" {
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}
	return m.fallback
}

func (m *Manager) cached(key string) (bool, bool) {
	if m.ttl <= 0 {
		return false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[key]
	if !ok {
		return false, false
	}
	if m.nowFn().Sub(e.at) > m.ttl {
		return false, false
	}
	return e.enabled, true
}
