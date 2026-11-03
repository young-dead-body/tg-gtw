package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const CookieName = "tg_session"

var ErrNotFound = errors.New("session not found")

type Store interface {
	Create(context.Context, string, time.Duration) (string, error)
	Get(context.Context, string) (string, error)
	Delete(context.Context, string) error
	DeleteUser(context.Context, string) error
}

// VersionedStore binds sessions to the current database credential revision.
// This rejects stale sessions even if Redis revocation failed or raced login.
type VersionedStore interface {
	CreateVersioned(context.Context, string, time.Duration, string) (string, error)
	Revision(context.Context, string) (string, error)
}

func CredentialRevision(hash string, changedAt time.Time) string {
	sum := sha256.Sum256([]byte(hash + "|" + changedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

type Info struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	Current   bool      `json:"current"`
}

type Manager interface {
	List(context.Context, string) ([]Info, error)
	DeleteOwned(context.Context, string, string) (bool, error)
}

type RedisStore struct{ client *redis.Client }

func NewRedisStore(client *redis.Client) *RedisStore { return &RedisStore{client: client} }

const keyPrefix = "session:"
const userKeyPrefix = "user_sessions_v2:"

func sessionKey(id string) string { return keyPrefix + id }
func userKey(id string) string    { return userKeyPrefix + id }

// Migrate the old unbounded set lazily, preserving all still-live sessions.
// Redis time keeps scores consistent across API hosts with clock skew.
const indexScript = `
local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)
local old = redis.call('SMEMBERS', KEYS[2])
for _, id in ipairs(old) do
 local ttl = redis.call('PTTL', 'session:' .. id)
 if ttl > 0 then redis.call('ZADD', KEYS[1], now + ttl, id) end
end
redis.call('DEL', KEYS[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
`
const expireIndexScript = `
local last = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
if #last > 0 then redis.call('PEXPIRE', KEYS[1], math.max(1, last[2] - now)) end
`

var createScript = redis.NewScript(indexScript + `
redis.call('SET', KEYS[3], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[4], ARGV[4], 'PX', ARGV[3])
redis.call('ZADD', KEYS[1], now + ARGV[3], ARGV[2])
` + expireIndexScript + `return 1`)

func (s *RedisStore) Create(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	return s.CreateVersioned(ctx, userID, ttl, "")
}
func (s *RedisStore) CreateVersioned(ctx context.Context, userID string, ttl time.Duration, revision string) (string, error) {
	if ttl < time.Millisecond {
		return "", errors.New("session TTL must be positive")
	}
	id := uuid.NewString()
	err := createScript.Run(ctx, s.client, []string{userKey(userID), "user_sessions:" + userID, sessionKey(id), "session_revision:" + id}, userID, id, ttl.Milliseconds(), revision).Err()
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return id, nil
}
func (s *RedisStore) Get(ctx context.Context, id string) (string, error) {
	uid, err := s.client.Get(ctx, sessionKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}
	return uid, nil
}

var deleteScript = redis.NewScript(`
local uid = redis.call('GET', KEYS[1])
if not uid then return 0 end
if ARGV[2] ~= '' and uid ~= ARGV[2] then return 0 end
redis.call('DEL', KEYS[1], 'session_revision:' .. ARGV[1])
redis.call('ZREM', 'user_sessions_v2:' .. uid, ARGV[1])
redis.call('SREM', 'user_sessions:' .. uid, ARGV[1])
return 1
`)

func (s *RedisStore) Delete(ctx context.Context, id string) error {
	return deleteScript.Run(ctx, s.client, []string{sessionKey(id)}, id, "").Err()
}

// PublicID is a management handle, never a bearer session credential.
func PublicID(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:])
}
func (s *RedisStore) DeleteOwned(ctx context.Context, uid, handle string) (bool, error) {
	if _, err := s.List(ctx, uid); err != nil {
		return false, err
	}
	ids, err := s.client.ZRange(ctx, userKey(uid), 0, -1).Result()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if PublicID(id) == handle {
			n, err := deleteScript.Run(ctx, s.client, []string{sessionKey(id)}, id, uid).Int()
			return n == 1, err
		}
	}
	return false, nil
}

var deleteUserScript = redis.NewScript(indexScript + `
local ids = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, id in ipairs(ids) do redis.call('DEL', 'session:' .. id, 'session_revision:' .. id) end
redis.call('DEL', KEYS[1])
return 1
`)

func (s *RedisStore) DeleteUser(ctx context.Context, uid string) error {
	return deleteUserScript.Run(ctx, s.client, []string{userKey(uid), "user_sessions:" + uid}).Err()
}

var listScript = redis.NewScript(indexScript + expireIndexScript + `return redis.call('ZRANGE', KEYS[1], 0, -1, 'WITHSCORES')`)

func (s *RedisStore) List(ctx context.Context, uid string) ([]Info, error) {
	rows, err := listScript.Run(ctx, s.client, []string{userKey(uid), "user_sessions:" + uid}).StringSlice()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(rows)/2)
	for i := 0; i < len(rows); i += 2 {
		var millis int64
		if _, err := fmt.Sscan(rows[i+1], &millis); err != nil {
			return nil, err
		}
		out = append(out, Info{ID: PublicID(rows[i]), ExpiresAt: time.UnixMilli(millis).UTC()})
	}
	return out, nil
}

func (s *RedisStore) Revision(ctx context.Context, id string) (string, error) {
	value, err := s.client.Get(ctx, "session_revision:"+id).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNotFound
	}
	return value, err
}
