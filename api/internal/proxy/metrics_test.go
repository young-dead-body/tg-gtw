package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackendGaugeRecordsReachability(t *testing.T) {
	BackendGauge("http://tg-a:8080", true)
	BackendGauge("http://tg-b:8080", false)

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	text := string(body)
	if !strings.Contains(text, `telegram_gateway_proxy_backend_active{url="http://tg-a:8080"} 1`) {
		t.Errorf("expected tg-a active, got:\n%s", text)
	}
	if !strings.Contains(text, `telegram_gateway_proxy_backend_active{url="http://tg-b:8080"} 0`) {
		t.Errorf("expected tg-b inactive, got:\n%s", text)
	}
}

func TestRewriteResultCounter(t *testing.T) {
	RewriteResult("bundle", true)
	RewriteResult("bundle", false)
	RewriteResult("config", true)

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	text := string(body)
	// The counters are cumulative across the package's tests, so only assert
	// that every recorded outcome is present (value >= 1).
	for _, want := range []string{
		`telegram_gateway_proxy_rewrite_total{kind="bundle",result="hit"}`,
		`telegram_gateway_proxy_rewrite_total{kind="bundle",result="miss"}`,
		`telegram_gateway_proxy_rewrite_total{kind="config",result="hit"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q, got:\n%s", want, text)
		}
	}
}

func TestObserveHandlerRecordsRequests(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(ObserveHandler(inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/some/tg/path")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	text, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(text), `telegram_gateway_proxy_requests_total{method="GET"}`) {
		t.Errorf("expected proxy request counter, got:\n%s", text)
	}
}
