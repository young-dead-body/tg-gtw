package policy

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
)

// SettingGetter reads a settings row by key. It is a subset of db.Querier so
// policy can be used from both the app and the cron packages without cycles.
type SettingGetter interface {
	GetSetting(ctx context.Context, key string) (db.Setting, error)
}

// Password carries the password rotation policy (ТЗ §8, §17).
type Password struct {
	// RotationPeriod is how long a password stays valid before it must change.
	RotationPeriod time.Duration
	// WarningWindow is how long before expiry users are warned to rotate.
	WarningWindow time.Duration
}

// DefaultPassword returns the fallback policy when settings are absent.
func DefaultPassword() Password {
	return Password{
		RotationPeriod: 90 * 24 * time.Hour,
		WarningWindow:  14 * 24 * time.Hour,
	}
}

const (
	// PasswordRotationPeriodKey is the settings key, in days.
	PasswordRotationPeriodKey = "password.rotation_period_days"
	// PasswordWarningWindowKey is the settings key, in days.
	PasswordWarningWindowKey = "password.warning_days"
)

// LoadPassword reads the rotation policy from settings, falling back to
// defaults for missing or malformed values.
func LoadPassword(ctx context.Context, g SettingGetter) (Password, error) {
	p := DefaultPassword()

	if days, err := readDays(ctx, g, PasswordRotationPeriodKey); err == nil && days > 0 {
		p.RotationPeriod = time.Duration(days) * 24 * time.Hour
	}
	if days, err := readDays(ctx, g, PasswordWarningWindowKey); err == nil && days >= 0 {
		p.WarningWindow = time.Duration(days) * 24 * time.Hour
	}

	return p, nil
}

// readDays reads an integer setting stored either as a plain JSON number or as
// a JSON string.
func readDays(ctx context.Context, g SettingGetter, key string) (int, error) {
	s, err := g.GetSetting(ctx, key)
	if err != nil {
		return 0, err
	}
	var raw any
	if err := json.Unmarshal(s.Value, &raw); err != nil {
		return 0, err
	}
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, &strconv.NumError{Func: "readDays", Num: string(s.Value), Err: strconv.ErrSyntax}
	}
}
