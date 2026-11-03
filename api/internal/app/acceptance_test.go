package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/session"
)

// startAcceptanceServer boots the app on a real TCP listener and returns its
// base URL, exercising the true HTTP pipeline (ТЗ §18 T18.2).
func startAcceptanceServer(t *testing.T) string {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	hasher := password.New()
	hash, err := hasher.Hash("LoadTestPass123!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: hash, Valid: true}
	user.CreatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	queries := &fakeQuerier{}
	queries.getByEmailFn = func(_ context.Context, _ string) (db.User, error) {
		return user, nil
	}

	srv := &Server{
		Queries:  queries,
		Sessions: session.NewRedisStore(rdb),
		Hasher:   hasher,
		Notifier: newFakeNotifier(),
		Config:   Config{SessionTTL: time.Hour},
	}
	app := New(srv, HealthCheckers{})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Listener(ln) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	return "http://" + ln.Addr().String()
}

func loginReq(email, password string) (*http.Request, error) {
	body := strings.NewReader(`{"email":"` + email + `","password":"` + password + `"}`)
	req, err := http.NewRequest(http.MethodPost, "http://0/login", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	return req, nil
}

func withBase(req *http.Request, base string) error {
	u, err := url.Parse(base + "/login")
	if err != nil {
		return err
	}
	req.URL = u
	return nil
}

// TestLoginLatencyUnderOneSecond measures the real authorization path
// (Argon2id verify + Redis session create) concurrently and checks the ТЗ §18
// bound: authorization ≤ 1 s.
//
// Each login attempt runs a full Argon2id verification (64 MiB, p=1), so the
// burst size is capped so the test stays deterministic on CI without exhausting
// memory; the 500-session capacity is covered by TestConcurrentSessionCapacity
// and by the loadgen tool for a full fleet.
func TestLoginLatencyUnderOneSecond(t *testing.T) {
	const clients = 16
	base := startAcceptanceServer(t)
	client := &http.Client{Timeout: 10 * time.Second}

	var wg sync.WaitGroup
	lats := make([]time.Duration, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, _ := loginReq("ivan@example.com", "LoadTestPass123!")
			_ = withBase(req, base)
			start := time.Now()
			resp, err := client.Do(req)
			lats[idx] = time.Since(start)
			if err != nil {
				t.Errorf("req %d: %v", idx, err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("req %d status=%d", idx, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()

	sort.Slice(lats, func(a, b int) bool { return lats[a] < lats[b] })
	p50 := lats[int(0.50*float64(len(lats)))]
	p95 := lats[int(0.95*float64(len(lats)))]
	t.Logf("real-login clients=%d p50=%s p95=%s max=%s", clients, p50, p95, lats[len(lats)-1])
	if p95 > time.Second {
		t.Errorf("p95 authorization latency %s exceeds ТЗ bound of 1s", p95)
	}
}

// TestConcurrentSessionCapacity proves the stateless (Redis-backed) session
// store keeps at least 500 simultaneous sessions alive, which is the ТЗ §18
// capacity requirement for concurrent users.
func TestConcurrentSessionCapacity(t *testing.T) {
	const sessions = 500
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := session.NewRedisStore(rdb)

	var wg sync.WaitGroup
	ids := make([]string, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id, err := store.Create(context.Background(), "user-1", time.Hour)
			if err != nil {
				t.Errorf("create session %d: %v", idx, err)
				return
			}
			ids[idx] = id
		}(i)
	}
	wg.Wait()

	for i, id := range ids {
		if id == "" {
			t.Fatalf("session %d missing", i)
		}
		uid, err := store.Get(context.Background(), id)
		if err != nil || uid != "user-1" {
			t.Errorf("session %d: get %q err=%v", i, uid, err)
		}
	}
	t.Logf("sessions=%d stored and read back", sessions)
}
