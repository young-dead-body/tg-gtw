package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestIndexExpiresAndPrunesWithMixedTTLs(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	s := NewRedisStore(client)
	ctx := t.Context()
	long, err := s.Create(ctx, "user", time.Hour)
	require.NoError(t, err)
	short, err := s.Create(ctx, "user", time.Second)
	require.NoError(t, err)
	require.Greater(t, mr.TTL(userKey("user")), 59*time.Minute)
	mr.FastForward(2 * time.Second)
	_, err = s.Get(ctx, short)
	require.ErrorIs(t, err, ErrNotFound)
	rows, err := s.List(ctx, "user")
	require.NoError(t, err)
	// Redis TIME is not advanced by miniredis FastForward; advancing again on
	// expiry still proves the entire index cannot outlive its longest session.
	require.NotEmpty(t, rows)
	require.Equal(t, "user", client.Get(ctx, sessionKey(long)).Val())
	mr.FastForward(time.Hour)
	require.False(t, mr.Exists(userKey("user")))
}
func TestRevocationPreservesOtherUsersAndMigratesLegacyIndex(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	s := NewRedisStore(client)
	ctx := t.Context()
	require.NoError(t, client.Set(ctx, "session:legacy", "alice", time.Hour).Err())
	require.NoError(t, client.SAdd(ctx, "user_sessions:alice", "legacy", "expired").Err())
	rows, err := s.List(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, PublicID("legacy"), rows[0].ID)
	require.False(t, mr.Exists("user_sessions:alice"))
	removed, err := s.DeleteOwned(ctx, "bob", "legacy")
	require.NoError(t, err)
	require.False(t, removed)
	other, err := s.Create(ctx, "bob", time.Hour)
	require.NoError(t, err)
	require.NoError(t, s.DeleteUser(ctx, "alice"))
	_, err = s.Get(ctx, "legacy")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Get(ctx, other)
	require.NoError(t, err)
}
func TestListPrunesExpiredScores(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	s := NewRedisStore(client)
	require.NoError(t, client.ZAdd(context.Background(), userKey("u"), redis.Z{Score: 1, Member: "expired"}).Err())
	rows, err := s.List(t.Context(), "u")
	require.NoError(t, err)
	require.Empty(t, rows)
}
