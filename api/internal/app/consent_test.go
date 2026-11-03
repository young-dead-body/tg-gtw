package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestConsentInfoReturnsDefaultVersion(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodGet, "/consent", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["document_version"] != defaultConsentDocumentVersion {
		t.Errorf("expected default version %q, got %v", defaultConsentDocumentVersion, body["document_version"])
	}
}

func TestConsentInfoUsesConfiguredVersion(t *testing.T) {
	h := buildHarness(t)
	h.queries.getSettingFn = func(ctx context.Context, key string) (db.Setting, error) {
		return db.Setting{Key: key, Value: []byte(`"2.1"`)}, nil
	}
	resp, err := h.call(http.MethodGet, "/consent", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body := decodeBody(t, resp)
	if body["document_version"] != "2.1" {
		t.Errorf("expected 2.1, got %v", body["document_version"])
	}
}

func TestConsentRejectedWhenNotAccepted(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/consent", `{"accepted":false}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestConsentRecorded(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/consent",
		`{"accepted":true,"document_version":"1.0"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["document_version"] != "1.0" {
		t.Errorf("expected version 1.0, got %v", body["document_version"])
	}
	if body["accepted_at"] == nil {
		t.Error("expected accepted_at timestamp")
	}
}
