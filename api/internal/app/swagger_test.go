package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/company/telegram-gateway/api/internal/password"
)

// specForSwagger returns a bare app with the /swagger.json, /swagger and
// /swagger-ui.js routes (no DB-backed handlers needed).
func specForSwagger() *fiber.App {
	return New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: &fakeSessionStore{},
		Hasher:   password.New(),
		Notifier: newFakeNotifier(),
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})
}

func TestSwaggerContractIsValidOpenAPI(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/swagger.json", nil)
	resp, err := specForSwagger().Test(req, 1000)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title string `json:"title"`
		} `json:"info"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("expected openapi 3.1.0, got %q", doc.OpenAPI)
	}
	if doc.Info.Title == "" {
		t.Error("expected a title")
	}
	for _, path := range []string{"/login", "/users", "/registration", "/stats", "/logs", "/me", "/consent"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("expected path %q in OpenAPI spec", path)
		}
	}
}

func TestSwaggerUIAndAssets(t *testing.T) {
	app := specForSwagger()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/swagger", nil), 1000)
	if err != nil {
		t.Fatalf("ui request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for UI, got %d", resp.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "swagger-ui.js") {
		t.Error("UI page must reference the renderer script")
	}

	jsResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/swagger-ui.js", nil), 1000)
	if err != nil {
		t.Fatalf("js request: %v", err)
	}
	if jsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for js, got %d", jsResp.StatusCode)
	}
}
