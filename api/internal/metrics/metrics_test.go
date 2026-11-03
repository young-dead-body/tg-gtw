package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestHandlerExposesPrometheusText(t *testing.T) {
	m := New()
	m.SetPostgresUp(true)
	m.SetRedisUp(false)
	m.SetProxyUp(true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	text := string(body)

	for _, want := range []string{
		"telegram_gateway_dependencies_postgres_up 1",
		"telegram_gateway_dependencies_redis_up 0",
		"telegram_gateway_dependencies_telegram_proxy_up 1",
		"go_goroutines",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected metric %q in output, got:\n%s", want, text)
		}
	}
}

func TestFiberMiddlewareRecordsRequest(t *testing.T) {
	m := New()
	app := fiber.New()
	app.Use(m.FiberMiddleware)
	app.Get("/hello", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/hello", nil), 1000)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	text, _ := io.ReadAll(rec.Result().Body)

	if !strings.Contains(string(text), `telegram_gateway_http_requests_total{method="GET",route="/hello",status="200"} 1`) {
		t.Errorf("expected request counter recorded, got:\n%s", text)
	}
}
