package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/ratelimit"
)

func hashText(plain string) pgtype.Text {
	return pgtype.Text{String: "hash:" + plain, Valid: true}
}

func httpReq(method, path, body string, cookie *http.Cookie) *http.Request {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func newTestApp(fq *fakeQuerier, rate *ratelimit.Limiter, rateLogin *ratelimit.Limiter, lockout *ratelimit.Lockout) *fiber.App {
	if fq == nil {
		fq = &fakeQuerier{}
	}
	return New(&Server{
		Queries:   fq,
		Sessions:  &fakeSessionStore{sessions: map[string]string{"sess_admin": fakeAdmin.ID.String()}},
		Hasher:    testHasher(),
		Notifier:  &fakeNotifier{},
		RateAPI:   rate,
		RateLogin: rateLogin,
		Lockout:   lockout,
		Config:    Config{SessionTTL: time.Hour},
	}, HealthCheckers{})
}

func TestCSRFMissingTokenRejected(t *testing.T) {
	app := newTestApp(&fakeQuerier{}, nil, nil, nil)

	req := httpReq(http.MethodPost, "/login", `{"email":"a@b.c","password":"x"}`, nil)
	req.Header.Set("X-CSRF-Token", "some-token")
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without csrf cookie, got %d", resp.StatusCode)
	}
}

func TestCSRFMismatchRejected(t *testing.T) {
	app := newTestApp(&fakeQuerier{}, nil, nil, nil)

	req := httpReq(http.MethodPost, "/login", `{"email":"a@b.c","password":"x"}`, &http.Cookie{Name: CSRFCookieName, Value: "cookie-token"})
	req.Header.Set("X-CSRF-Token", "other-token")
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on mismatch, got %d", resp.StatusCode)
	}
}

func TestCSRFIssuedOnGet(t *testing.T) {
	app := newTestApp(&fakeQuerier{}, nil, nil, nil)

	req := httpReq(http.MethodGet, "/consent", "", nil)
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == CSRFCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected tg_csrf cookie on GET")
	}
}

func TestCSRFTokenAccepted(t *testing.T) {
	app := newTestApp(&fakeQuerier{}, nil, nil, nil)

	req := httpReq(http.MethodPost, "/login", `{"email":"a@b.c","password":"x"}`, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected login to proceed (401 for unknown user), got %d", resp.StatusCode)
	}
}

func TestRateLimitLogin(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.NewLimiter(store, "test:login:", 2, time.Minute)
	app := newTestApp(&fakeQuerier{}, nil, limiter, nil)

	var last *http.Response
	for i := 0; i < 3; i++ {
		req := httpReq(http.MethodPost, "/login", `{"email":"a@b.c","password":"x"}`, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
		req.Header.Set("X-CSRF-Token", testCSRFToken)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		last = resp
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on third attempt, got %d", last.StatusCode)
	}
}

func TestRateLimitAPI(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.NewLimiter(store, "test:api:", 2, time.Minute)
	app := newTestApp(&fakeQuerier{}, limiter, nil, nil)

	var last *http.Response
	for i := 0; i < 3; i++ {
		req := httpReq(http.MethodGet, "/consent", "", nil)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		last = resp
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on third request, got %d", last.StatusCode)
	}
}

func TestLockoutBlocksLogin(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	lockout := ratelimit.NewLockout(store, 3, 15*time.Minute)
	app := newTestApp(&fakeQuerier{}, nil, nil, lockout)

	body := `{"email":"ivan@example.com","password":"wrong"}`
	for i := 0; i < 3; i++ {
		req := httpReq(http.MethodPost, "/login", body, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
		req.Header.Set("X-CSRF-Token", testCSRFToken)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if i < 2 && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i, resp.StatusCode)
		}
	}

	req := httpReq(http.MethodPost, "/login", body, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("attempt 4: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after lockout threshold, got %d", resp.StatusCode)
	}
}

func TestLockoutResetOnSuccess(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	lockout := ratelimit.NewLockout(store, 3, 15*time.Minute)
	fq := &fakeQuerier{}
	fq.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		u := fakeUser
		u.PasswordHash = hashText("correct")
		return u, nil
	}
	app := newTestApp(fq, nil, nil, lockout)

	fail := func() *http.Response {
		req := httpReq(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"wrong"}`, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
		req.Header.Set("X-CSRF-Token", testCSRFToken)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		return resp
	}
	login := func() *http.Response {
		req := httpReq(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"correct"}`, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
		req.Header.Set("X-CSRF-Token", testCSRFToken)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		return resp
	}

	// Two failures bring the counters to 2 of 3 — not yet blocked.
	for i := 0; i < 2; i++ {
		if resp := fail(); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i, resp.StatusCode)
		}
	}
	// A successful login must clear the counters (T15.2).
	if resp := login(); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Failures now accumulate from scratch (threshold 3): the counters were
	// reset, so the first three failures still pass.
	for i := 0; i < 3; i++ {
		if resp := fail(); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d after reset: expected 401, got %d", i, resp.StatusCode)
		}
	}
	if resp := fail(); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once threshold reached again, got %d", resp.StatusCode)
	}
}
