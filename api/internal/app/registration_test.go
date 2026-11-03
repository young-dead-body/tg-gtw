package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func validRegistrationBody() string {
	return `{
		"first_name": "Пётр",
		"last_name": "Петров",
		"middle_name": "Петрович",
		"email": "petr@example.com",
		"telegram": "@petrov",
		"department": "IT",
		"position": "Разработчик",
		"reason": "Доступ для работы с каналом проекта"
	}`
}

func TestRegistrationSuccess(t *testing.T) {
	h := buildHarness(t)

	resp, err := h.call(http.MethodPost, "/registration", validRegistrationBody(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, readAll(t, resp))
	}

	body := decodeBody(t, resp)
	req := body["request"].(map[string]any)
	if req["status"] != "pending" {
		t.Errorf("expected status pending, got %v", req["status"])
	}
	if req["email"] != "petr@example.com" {
		t.Errorf("unexpected email %v", req["email"])
	}

	// The notifier must have been invoked for the admin.
	if len(h.notifier.events["registration.created"]) != 1 {
		t.Errorf("expected notifier event, got %v", h.notifier.events)
	}
}

func TestRegistrationRequiresMandatoryFields(t *testing.T) {
	h := buildHarness(t)

	resp, err := h.call(http.MethodPost, "/registration", `{
		"first_name": "",
		"last_name": "",
		"email": "",
		"reason": ""
	}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	body := decodeBody(t, resp)
	errorsList, ok := body["errors"].([]any)
	if !ok {
		t.Fatalf("expected errors array, got %v", body)
	}
	fields := map[string]bool{}
	for _, e := range errorsList {
		fields[e.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"first_name", "last_name", "email", "reason"} {
		if !fields[want] {
			t.Errorf("expected validation error for %q, got %v", want, fields)
		}
	}
}

func TestRegistrationInvalidEmail(t *testing.T) {
	h := buildHarness(t)

	resp, _ := h.call(http.MethodPost, "/registration", `{
		"first_name": "Пётр",
		"last_name": "Петров",
		"email": "not-an-email",
		"reason": "test"
	}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	errorsList := body["errors"].([]any)
	if errorsList[0].(map[string]any)["field"] != "email" {
		t.Errorf("expected email error, got %v", errorsList)
	}
}

func TestRegistrationInvalidTelegram(t *testing.T) {
	h := buildHarness(t)

	resp, _ := h.call(http.MethodPost, "/registration", `{
		"first_name": "Пётр",
		"last_name": "Петров",
		"email": "petr@example.com",
		"telegram": "@a!b",
		"reason": "test"
	}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	errorsList := body["errors"].([]any)
	if errorsList[0].(map[string]any)["field"] != "telegram" {
		t.Errorf("expected telegram error, got %v", errorsList)
	}
}

func TestRegistrationInvalidBody(t *testing.T) {
	h := buildHarness(t)

	resp, _ := h.call(http.MethodPost, "/registration", `{"first_name":`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRegistrationSavesAllFields(t *testing.T) {
	h := buildHarness(t)
	var captured db.CreateRegistrationRequestParams
	h.queries.createRegistrationFn = func(ctx context.Context, arg db.CreateRegistrationRequestParams) (db.RegistrationRequest, error) {
		captured = arg
		return db.RegistrationRequest{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Status: "pending"}, nil
	}

	resp, err := h.call(http.MethodPost, "/registration", validRegistrationBody(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	if captured.FirstName != "Пётр" || captured.LastName != "Петров" || captured.Email != "petr@example.com" {
		t.Errorf("unexpected identity fields: %+v", captured)
	}
	if !captured.MiddleName.Valid || captured.MiddleName.String != "Петрович" {
		t.Errorf("expected middle_name, got %v", captured.MiddleName)
	}
	if !captured.Telegram.Valid || captured.Telegram.String != "@petrov" {
		t.Errorf("expected telegram, got %v", captured.Telegram)
	}
	if !captured.Department.Valid || captured.Department.String != "IT" {
		t.Errorf("expected department, got %v", captured.Department)
	}
	if !captured.Position.Valid || captured.Position.String != "Разработчик" {
		t.Errorf("expected position, got %v", captured.Position)
	}
	if captured.Reason.String == "" {
		t.Error("expected reason")
	}
}
