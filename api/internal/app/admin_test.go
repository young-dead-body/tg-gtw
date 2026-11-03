package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestDashboardMetrics(t *testing.T) {
	resp, err := doRequestAdmin(t, &fakeQuerier{}, http.MethodGet, "/dashboard", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body dashboardData
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UsersCount != 12 || body.BlockedUsersCount != 1 || body.PendingRequests != 2 {
		t.Errorf("unexpected user metrics: %+v", body)
	}
	if body.LoginsToday != 5 || body.LoginsTotal != 42 {
		t.Errorf("unexpected login metrics: %+v", body)
	}
}

func TestDashboardRequiresAdmin(t *testing.T) {
	app := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: &fakeSessionStore{sessions: map[string]string{"sess_user": fakeUser.ID.String()}},
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: 1},
	}, HealthCheckers{})

	resp, err := doRequestWithApp(t, app, http.MethodGet, "/dashboard", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous, got %d", resp.StatusCode)
	}

	resp, err = doRequestWithApp(t, app, http.MethodGet, "/dashboard", "",
		[]*http.Cookie{{Name: CookieName, Value: "sess_user"}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for regular user, got %d", resp.StatusCode)
	}
}

func TestRegistrationRequestsList(t *testing.T) {
	fq := &fakeQuerier{}
	fq.listRegistrationFn = func(ctx context.Context) ([]db.RegistrationRequest, error) {
		return []db.RegistrationRequest{
			{
				ID:        pgtype.UUID{Bytes: [16]byte{7}, Valid: true},
				FirstName: "Анна",
				LastName:  "Петрова",
				Email:     "anna@example.com",
				Status:    "pending",
			},
		}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodGet, "/registration-requests", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Requests []fiber.Map `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(body.Requests))
	}
	if body.Requests[0]["email"] != "anna@example.com" || body.Requests[0]["status"] != "pending" {
		t.Errorf("unexpected request: %+v", body.Requests[0])
	}
}

func TestRegistrationRequestApproveCreatesUser(t *testing.T) {
	fq := &fakeQuerier{}
	fq.getRegistrationFn = func(ctx context.Context, id pgtype.UUID) (db.RegistrationRequest, error) {
		return db.RegistrationRequest{
			ID:         id,
			FirstName:  "Анна",
			LastName:   "Петрова",
			MiddleName: pgtype.Text{String: "Ивановна", Valid: true},
			Email:      "anna@example.com",
			Telegram:   pgtype.Text{String: "petrova", Valid: true},
			Department: pgtype.Text{String: "IT", Valid: true},
			Position:   pgtype.Text{String: "dev", Valid: true},
			Status:     "pending",
		}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodPatch, "/registration-requests/"+fakeUser.ID.String(),
		`{"status":"approved"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(fq.createdUsers) != 1 {
		t.Fatalf("expected 1 created user, got %d", len(fq.createdUsers))
	}
	u := fq.createdUsers[0]
	if u.Email != "anna@example.com" || u.Role != "user" || u.Telegram.String != "petrova" {
		t.Errorf("unexpected created user: %+v", u)
	}

	if len(fq.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(fq.auditLogs))
	}
	if fq.auditLogs[0].Action != auditRegistrationApproved {
		t.Errorf("expected %s, got %s", auditRegistrationApproved, fq.auditLogs[0].Action)
	}
	if fq.auditLogs[0].UserID.Bytes != fakeAdmin.ID.Bytes {
		t.Errorf("expected admin actor")
	}
}

func TestRegistrationRequestRejectDoesNotCreateUser(t *testing.T) {
	fq := &fakeQuerier{}
	fq.getRegistrationFn = func(ctx context.Context, id pgtype.UUID) (db.RegistrationRequest, error) {
		return db.RegistrationRequest{ID: id, Email: "anna@example.com", Status: "pending"}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodPatch, "/registration-requests/"+fakeUser.ID.String(),
		`{"status":"rejected"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(fq.createdUsers) != 0 {
		t.Fatalf("expected no created user, got %d", len(fq.createdUsers))
	}
	if len(fq.auditLogs) != 1 || fq.auditLogs[0].Action != auditRegistrationRejected {
		t.Errorf("expected %s audit row", auditRegistrationRejected)
	}
}

func TestRegistrationRequestAlreadyReviewed(t *testing.T) {
	fq := &fakeQuerier{}
	fq.getRegistrationFn = func(ctx context.Context, id pgtype.UUID) (db.RegistrationRequest, error) {
		return db.RegistrationRequest{ID: id, Email: "anna@example.com", Status: "approved"}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodPatch, "/registration-requests/"+fakeUser.ID.String(),
		`{"status":"approved"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestRegistrationRequestInvalidStatus(t *testing.T) {
	resp, err := doRequestAdmin(t, &fakeQuerier{}, http.MethodPatch, "/registration-requests/"+fakeUser.ID.String(),
		`{"status":"maybe"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSettingsGet(t *testing.T) {
	fq := &fakeQuerier{}
	fq.getSettingFn = func(ctx context.Context, key string) (db.Setting, error) {
		return db.Setting{Key: key, Value: []byte(`"1.5"`)}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodGet, "/settings/consent.document_version", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Key != "consent.document_version" || body.Value != "1.5" {
		t.Errorf("unexpected setting: %+v", body)
	}
}

func TestSettingsGetUnknownKey(t *testing.T) {
	resp, err := doRequestAdmin(t, &fakeQuerier{}, http.MethodGet, "/settings/some.key", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSettingsPut(t *testing.T) {
	fq := &fakeQuerier{}
	resp, err := doRequestAdmin(t, fq, http.MethodPut, "/settings/consent.document_version",
		`{"value":"2.0"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(fq.auditLogs) != 1 || fq.auditLogs[0].Action != auditSettings {
		t.Errorf("expected settings.updated audit row")
	}
	if fq.auditLogs[0].EntityID.String != "consent.document_version" {
		t.Errorf("expected setting key in entity_id")
	}
}

func TestSettingsPutEmptyValue(t *testing.T) {
	resp, err := doRequestAdmin(t, &fakeQuerier{}, http.MethodPut, "/settings/consent.document_version",
		`{"value":""}`, adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
