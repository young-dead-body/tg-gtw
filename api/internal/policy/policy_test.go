package policy

import (
	"context"
	"testing"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
)

type fakeSettings map[string][]byte

func (f fakeSettings) GetSetting(_ context.Context, key string) (db.Setting, error) {
	if v, ok := f[key]; ok {
		return db.Setting{Key: key, Value: v}, nil
	}
	return db.Setting{}, &notFoundError{key: key}
}

type notFoundError struct{ key string }

func (e *notFoundError) Error() string { return "not found: " + e.key }

func TestDefaultPassword(t *testing.T) {
	p := DefaultPassword()
	if p.RotationPeriod != 90*24*time.Hour {
		t.Errorf("unexpected default rotation %v", p.RotationPeriod)
	}
	if p.WarningWindow != 14*24*time.Hour {
		t.Errorf("unexpected default warning window %v", p.WarningWindow)
	}
}

func TestLoadPasswordDefaultsWhenUnset(t *testing.T) {
	ctx := context.Background()
	p, err := LoadPassword(ctx, fakeSettings{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p != DefaultPassword() {
		t.Errorf("expected defaults, got %+v", p)
	}
}

func TestLoadPasswordOverrides(t *testing.T) {
	ctx := context.Background()
	settings := fakeSettings{
		PasswordRotationPeriodKey: []byte(`30`),
		PasswordWarningWindowKey:  []byte(`"7"`),
	}
	p, err := LoadPassword(ctx, settings)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.RotationPeriod != 30*24*time.Hour {
		t.Errorf("expected 30 days, got %v", p.RotationPeriod)
	}
	if p.WarningWindow != 7*24*time.Hour {
		t.Errorf("expected 7 days, got %v", p.WarningWindow)
	}
}

func TestLoadPasswordIgnoresInvalidValues(t *testing.T) {
	ctx := context.Background()
	settings := fakeSettings{
		PasswordRotationPeriodKey: []byte(`"not-a-number"`),
		PasswordWarningWindowKey:  []byte(`{}`),
	}
	p, err := LoadPassword(ctx, settings)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p != DefaultPassword() {
		t.Errorf("expected defaults on invalid input, got %+v", p)
	}
}
