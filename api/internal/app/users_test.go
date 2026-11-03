package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

const adminRole = "admin"

var fakeAdmin = db.User{
	ID:        pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
	FirstName: "Админ",
	LastName:  "Админов",
	Email:     "admin@example.com",
	Role:      adminRole,
	IsActive:  true,
}

func adminCookies(t *testing.T) []*http.Cookie {
	t.Helper()
	return []*http.Cookie{{Name: CookieName, Value: "sess_admin"}}
}

func doRequestAdmin(t *testing.T, fq *fakeQuerier, method, path, body string, cookies []*http.Cookie) (*http.Response, error) {
	t.Helper()
	if fq == nil {
		fq = &fakeQuerier{}
	}
	application := New(&Server{
		Queries:  fq,
		Sessions: &fakeSessionStore{sessions: map[string]string{"sess_admin": fakeAdmin.ID.String()}},
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})
	return doRequestWithApp(t, application, method, path, body, cookies)
}

func testHasher() *passwordHasher {
	return &passwordHasher{}
}

type passwordHasher struct{}

func (passwordHasher) Hash(password string) (string, error) { return "hash:" + password, nil }
func (passwordHasher) Verify(password, hash string) (bool, error) {
	return hash == "hash:"+password, nil
}

func doRequestWithApp(t *testing.T, application interface {
	Test(req *http.Request, msTimeout ...int) (*http.Response, error)
}, method, path, body string, cookies []*http.Cookie) (*http.Response, error) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	cookies = addTestCSRF(method, req, cookies)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return application.Test(req, 1000)
}

func TestUsersRequireAuth(t *testing.T) {
	for _, m := range []struct{ method, path string }{
		{http.MethodGet, "/users"},
		{http.MethodPost, "/users"},
		{http.MethodPatch, "/users/" + fakeUser.ID.String()},
		{http.MethodDelete, "/users/" + fakeUser.ID.String()},
	} {
		resp, err := doRequestAdmin(t, nil, m.method, m.path, "", nil)
		if err != nil {
			t.Fatalf("app test: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for %s %s, got %d", m.method, m.path, resp.StatusCode)
		}
	}
}

func TestUsersForbiddenForRegularUser(t *testing.T) {
	// A regular user session must not reach admin endpoints.
	fq := &fakeQuerier{}
	cookies := []*http.Cookie{{Name: CookieName, Value: "sess_user"}}
	application := New(&Server{
		Queries:  fq,
		Sessions: &fakeSessionStore{sessions: map[string]string{"sess_user": fakeUser.ID.String()}},
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})

	for _, m := range []struct{ method, path string }{
		{http.MethodGet, "/users"},
		{http.MethodPost, "/users"},
		{http.MethodPatch, "/users/" + fakeUser.ID.String()},
		{http.MethodDelete, "/users/" + fakeUser.ID.String()},
	} {
		resp, err := doRequestWithApp(t, application, m.method, m.path, "", cookies)
		if err != nil {
			t.Fatalf("app test: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 for %s %s, got %d", m.method, m.path, resp.StatusCode)
		}
	}
}

func TestUsersListAdminOnly(t *testing.T) {
	resp, err := doRequestAdmin(t, nil, http.MethodGet, "/users", "", adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	users, ok := body["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("unexpected users payload: %+v", body)
	}
}

func TestUsersCreateValidation(t *testing.T) {
	// Missing required fields -> 400.
	resp, err := doRequestAdmin(t, nil, http.MethodPost, "/users", `{"first_name":"A"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUsersCreateSuccess(t *testing.T) {
	resp, err := doRequestAdmin(t, nil, http.MethodPost, "/users",
		`{"first_name":"Новый","last_name":"Пользователь","email":"new@example.com","role":"user","password":"strongpass123"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestUsersCreateRejectsBadRole(t *testing.T) {
	resp, err := doRequestAdmin(t, nil, http.MethodPost, "/users",
		`{"first_name":"А","last_name":"Б","email":"c@example.com","role":"superadmin"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUsersUpdatePasswordNotReused(t *testing.T) {
	// If the new password matches a recent one, the admin reset is rejected.
	fq := &fakeQuerier{
		listRecentFn: func(ctx context.Context, arg db.ListRecentPasswordHashesParams) ([]string, error) {
			return []string{"hash:oldpass123"}, nil
		},
	}
	resp, err := doRequestAdmin(t, fq, http.MethodPatch, "/users/"+fakeUser.ID.String(),
		`{"password":"oldpass123"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestUsersUpdateNotFound(t *testing.T) {
	fq := &fakeQuerier{
		getByIDFn: func(ctx context.Context, id pgtype.UUID) (db.User, error) {
			if id == fakeAdmin.ID {
				return fakeAdmin, nil
			}
			return db.User{}, pgx.ErrNoRows
		},
	}
	resp, err := doRequestAdmin(t, fq, http.MethodPatch, "/users/"+fakeUser.ID.String(), `{}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestUsersBlockRevokesSessions(t *testing.T) {
	store := &fakeSessionStore{sessions: map[string]string{"sess_admin": fakeAdmin.ID.String(), "sess_user": fakeUser.ID.String()}}
	application := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: store,
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})

	resp, err := doRequestWithApp(t, application, http.MethodPatch, "/users/"+fakeUser.ID.String(),
		`{"blocked":true}`, adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, ok := store.sessions["sess_user"]; ok {
		t.Error("expected user sessions to be revoked after blocking")
	}
	if _, ok := store.sessions["sess_admin"]; !ok {
		t.Error("admin session must survive")
	}
}

func TestUsersDelete(t *testing.T) {
	fq := &fakeQuerier{
		getByIDFn: func(ctx context.Context, id pgtype.UUID) (db.User, error) {
			if id == fakeAdmin.ID {
				return fakeAdmin, nil
			}
			return db.User{}, pgx.ErrNoRows
		},
		deleteUserFn: func(ctx context.Context, id pgtype.UUID) (db.User, error) {
			return db.User{}, pgx.ErrNoRows
		},
	}
	resp, err := doRequestAdmin(t, fq, http.MethodDelete, "/users/"+fakeUser.ID.String(), "", adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestUsersDeleteSuccess(t *testing.T) {
	store := &fakeSessionStore{sessions: map[string]string{"sess_admin": fakeAdmin.ID.String(), "sess_user": fakeUser.ID.String()}}
	application := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: store,
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})

	resp, err := doRequestWithApp(t, application, http.MethodDelete, "/users/"+fakeUser.ID.String(), "", adminCookies(t))
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, ok := store.sessions["sess_user"]; ok {
		t.Error("expected user sessions to be revoked on delete")
	}
}
