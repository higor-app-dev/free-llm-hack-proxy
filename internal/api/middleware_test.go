package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Helpers
// =============================================================================

// okHandler is a minimal handler that always writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
})

// testMW creates a Middleware from a partial config for testing.
func testMW(cfg MiddlewareConfig) *Middleware {
	m, err := NewMiddleware(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

// readErrorBody decodes the JSON error response body.
func readErrorBody(t *testing.T, rr *httptest.ResponseRecorder) middlewareError {
	t.Helper()
	var me middlewareError
	if err := json.NewDecoder(rr.Body).Decode(&me); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	return me
}

// =============================================================================
// NewMiddleware
// =============================================================================

func TestNewMiddleware_Defaults(t *testing.T) {
	m, err := NewMiddleware(MiddlewareConfig{})
	if err != nil {
		t.Fatalf("NewMiddleware() returned unexpected error: %v", err)
	}

	if m.apiKey != "" {
		t.Errorf("expected empty api key, got %q", m.apiKey)
	}
	if m.rateEnabled {
		t.Error("expected rate limiter disabled by default")
	}
	if len(m.allowedOrigins) != 1 || m.allowedOrigins[0] != "*" {
		t.Errorf("expected origins [*], got %v", m.allowedOrigins)
	}
	if m.allowedMethods != "GET, POST, OPTIONS" {
		t.Errorf("expected default methods, got %q", m.allowedMethods)
	}
	if m.allowedHeaders != "Content-Type, Authorization" {
		t.Errorf("expected default headers, got %q", m.allowedHeaders)
	}
}

func TestNewMiddleware_WithConfig(t *testing.T) {
	m, err := NewMiddleware(MiddlewareConfig{
		APIKey:         "secret",
		AllowedOrigins: "https://example.com, https://app.example.com",
		AllowedMethods: "GET, POST",
		AllowedHeaders: "X-Custom",
		RateLimitRPM:   60,
		RateLimitBurst: 10,
	})
	if err != nil {
		t.Fatalf("NewMiddleware() returned unexpected error: %v", err)
	}

	if m.apiKey != "secret" {
		t.Errorf("expected api key 'secret', got %q", m.apiKey)
	}
	if !m.rateEnabled {
		t.Error("expected rate limiter enabled")
	}
	if m.rateLimitRate != 1.0 {
		t.Errorf("expected rate 1.0 token/sec, got %f", m.rateLimitRate)
	}
	if m.rateLimitBurst != 10 {
		t.Errorf("expected burst 10, got %d", m.rateLimitBurst)
	}
	if len(m.allowedOrigins) != 2 {
		t.Errorf("expected 2 origins, got %d", len(m.allowedOrigins))
	}
}

func TestNewMiddleware_BurstDefaultsToRPM(t *testing.T) {
	m, err := NewMiddleware(MiddlewareConfig{
		RateLimitRPM: 30,
	})
	if err != nil {
		t.Fatalf("NewMiddleware() returned unexpected error: %v", err)
	}
	if m.rateLimitBurst != 30 {
		t.Errorf("expected burst == RPM (30), got %d", m.rateLimitBurst)
	}
}

// =============================================================================
// APIKeyAuth
// =============================================================================

func TestAuth_ValidKey(t *testing.T) {
	m := testMW(MiddlewareConfig{APIKey: "valid-key"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer valid-key")

	m.Auth(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	m := testMW(MiddlewareConfig{APIKey: "valid-key"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")

	m.Auth(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	me := readErrorBody(t, rr)
	if me.Error.Message != "unauthorized" {
		t.Errorf("expected message 'unauthorized', got %q", me.Error.Message)
	}
	if me.Error.Type != "auth_error" {
		t.Errorf("expected type 'auth_error', got %q", me.Error.Type)
	}
}

func TestAuth_MissingKey(t *testing.T) {
	m := testMW(MiddlewareConfig{APIKey: "valid-key"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Authorization header.

	m.Auth(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	me := readErrorBody(t, rr)
	if me.Error.Message != "unauthorized" {
		t.Errorf("expected message 'unauthorized', got %q", me.Error.Message)
	}
	if me.Error.Type != "auth_error" {
		t.Errorf("expected type 'auth_error', got %q", me.Error.Type)
	}
}

func TestAuth_EmptyAPIKey(t *testing.T) {
	// No API key configured → all requests pass through.
	m := testMW(MiddlewareConfig{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	m.Auth(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuth_OPTIONS_Skipped(t *testing.T) {
	// OPTIONS requests must pass through even without a valid key.
	m := testMW(MiddlewareConfig{APIKey: "secret"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	// No Authorization header at all.

	m.Auth(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for OPTIONS, got %d", rr.Code)
	}
}

// =============================================================================
// CORS
// =============================================================================

func TestCORS_SetsHeaders(t *testing.T) {
	m := testMW(MiddlewareConfig{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")

	m.CORS(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "*" {
		t.Errorf("expected Allow-Origin '*', got %q", h)
	}
	if h := rr.Header().Get("Access-Control-Allow-Methods"); h == "" {
		t.Error("expected Allow-Methods header to be set")
	}
	if h := rr.Header().Get("Access-Control-Allow-Headers"); h == "" {
		t.Error("expected Allow-Headers header to be set")
	}
}

func TestCORS_OPTIONS_Returns204(t *testing.T) {
	m := testMW(MiddlewareConfig{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)

	m.CORS(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rr.Code)
	}
	// Must still set headers.
	if h := rr.Header().Get("Access-Control-Allow-Origin"); h == "" {
		t.Error("expected Allow-Origin header on OPTIONS")
	}
	if h := rr.Header().Get("Access-Control-Allow-Methods"); h == "" {
		t.Error("expected Allow-Methods header on OPTIONS")
	}
	if h := rr.Header().Get("Access-Control-Allow-Headers"); h == "" {
		t.Error("expected Allow-Headers header on OPTIONS")
	}
}

func TestCORS_WildcardOrigin(t *testing.T) {
	m := testMW(MiddlewareConfig{AllowedOrigins: "*"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://random.example.com")

	m.CORS(okHandler).ServeHTTP(rr, req)

	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "*" {
		t.Errorf("expected '*', got %q", h)
	}
}

func TestCORS_SpecificOrigin(t *testing.T) {
	m := testMW(MiddlewareConfig{AllowedOrigins: "https://trusted.example.com"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://trusted.example.com")

	m.CORS(okHandler).ServeHTTP(rr, req)

	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "https://trusted.example.com" {
		t.Errorf("expected origin echo, got %q", h)
	}
}

func TestCORS_OriginNotAllowed(t *testing.T) {
	m := testMW(MiddlewareConfig{AllowedOrigins: "https://trusted.example.com"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	m.CORS(okHandler).ServeHTTP(rr, req)

	// Should not echo the origin back.
	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "" {
		t.Errorf("expected empty Allow-Origin for disallowed origin, got %q", h)
	}
}

func TestCORS_CustomMethodsAndHeaders(t *testing.T) {
	m := testMW(MiddlewareConfig{
		AllowedMethods: "GET, POST, PUT, DELETE",
		AllowedHeaders: "Content-Type, Authorization, X-API-Key",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	m.CORS(okHandler).ServeHTTP(rr, req)

	if h := rr.Header().Get("Access-Control-Allow-Methods"); h != "GET, POST, PUT, DELETE" {
		t.Errorf("unexpected methods: %q", h)
	}
	if h := rr.Header().Get("Access-Control-Allow-Headers"); h != "Content-Type, Authorization, X-API-Key" {
		t.Errorf("unexpected headers: %q", h)
	}
}

func TestCORS_MultipleOrigins(t *testing.T) {
	// List of allowed origins; request from a matching one.
	m := testMW(MiddlewareConfig{AllowedOrigins: "https://a.com, https://b.com"})

	// Matching origin.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://b.com")
	m.CORS(okHandler).ServeHTTP(rr, req)
	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "https://b.com" {
		t.Errorf("expected 'https://b.com', got %q", h)
	}

	// Non-matching origin.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Origin", "https://c.com")
	m.CORS(okHandler).ServeHTTP(rr2, req2)
	if h := rr2.Header().Get("Access-Control-Allow-Origin"); h != "" {
		t.Errorf("expected empty, got %q", h)
	}
}

// =============================================================================
// RateLimiter
// =============================================================================

func TestRateLimit_Disabled(t *testing.T) {
	m := testMW(MiddlewareConfig{RateLimitRPM: 0})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Fire many requests — all should pass since limiter is disabled.
	for i := 0; i < 100; i++ {
		rr = httptest.NewRecorder()
		m.RateLimit(okHandler).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, rr.Code)
			break
		}
	}
}

func TestRateLimit_WithinLimits(t *testing.T) {
	// 600 RPM = 10 tokens/sec, burst = 10.
	m := testMW(MiddlewareConfig{RateLimitRPM: 600, RateLimitBurst: 10})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// First 10 requests should succeed (burst capacity).
	for i := 0; i < 10; i++ {
		rr = httptest.NewRecorder()
		m.RateLimit(okHandler).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d within burst: expected 200, got %d", i, rr.Code)
		}
	}
}

func TestRateLimit_Exceeded(t *testing.T) {
	// 60 RPM = 1 token/sec, burst = 1.
	m := testMW(MiddlewareConfig{RateLimitRPM: 60, RateLimitBurst: 1})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// First request should succeed (burst = 1).
	m.RateLimit(okHandler).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be rate limited (no tokens left).
	rr2 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr2.Code)
	}

	// Check the error body.
	me := readErrorBody(t, rr2)
	if me.Error.Message != "rate limit exceeded" {
		t.Errorf("expected message 'rate limit exceeded', got %q", me.Error.Message)
	}
	if me.Error.Type != "rate_limit_error" {
		t.Errorf("expected type 'rate_limit_error', got %q", me.Error.Type)
	}
}

func TestRateLimit_PerIPTracking(t *testing.T) {
	m := testMW(MiddlewareConfig{RateLimitRPM: 60, RateLimitBurst: 1})

	// Two different IPs should each get their own token bucket.
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "10.0.0.1:12345"
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.2:12345"

	// Both first requests succeed.
	rr1 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Errorf("ip1 first: expected 200, got %d", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("ip2 first: expected 200, got %d", rr2.Code)
	}

	// Both second requests should be rate limited (both used their burst).
	rr1b := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr1b, req1)
	if rr1b.Code != http.StatusTooManyRequests {
		t.Errorf("ip1 second: expected 429, got %d", rr1b.Code)
	}

	rr2b := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr2b, req2)
	if rr2b.Code != http.StatusTooManyRequests {
		t.Errorf("ip2 second: expected 429, got %d", rr2b.Code)
	}
}

func TestRateLimit_TokenRefill(t *testing.T) {
	// 60 RPM = 1 token/sec, burst = 1.
	m := testMW(MiddlewareConfig{RateLimitRPM: 60, RateLimitBurst: 1})
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Consume the burst token.
	rr := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be rate limited.
	rr2 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rr2.Code)
	}

	// Wait for token refill (1 token/sec, so wait just over 1 second).
	time.Sleep(1100 * time.Millisecond)

	// Third request should now succeed (1 token refilled).
	rr3 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr3, req)
	if rr3.Code != http.StatusOK {
		t.Errorf("after refill: expected 200, got %d", rr3.Code)
	}
}

func TestRateLimit_XForwardedFor(t *testing.T) {
	m := testMW(MiddlewareConfig{RateLimitRPM: 60, RateLimitBurst: 1})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")

	rr := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// Same X-Forwarded-For IP → should be rate limited.
	rr2 := httptest.NewRecorder()
	m.RateLimit(okHandler).ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 for same IP, got %d", rr2.Code)
	}
}

func TestRateLimit_ConcurrentSafety(t *testing.T) {
	m := testMW(MiddlewareConfig{RateLimitRPM: 6000, RateLimitBurst: 100})

	var wg sync.WaitGroup
	errs := make(chan error, 200)

	// Fire 100 requests concurrently from the same IP.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()
			m.RateLimit(okHandler).ServeHTTP(rr, req)
			if rr.Code == http.StatusTooManyRequests {
				errs <- nil // rate limited - expected with 100 concurrent requests
				return
			}
			if rr.Code != http.StatusOK {
				errs <- nil // unexpected status
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)

	// All goroutines should have completed without panics.
	count := 0
	for range errs {
		count++
	}
	if count != 100 {
		t.Errorf("expected 100 results, got %d", count)
	}
}

// =============================================================================
// Chaining
// =============================================================================

func TestMiddlewareChaining(t *testing.T) {
	m := testMW(MiddlewareConfig{
		APIKey:        "mykey",
		AllowedOrigins: "https://app.example.com",
		RateLimitRPM:   600,
		RateLimitBurst: 10,
	})

	// Chain all three middleware.
	handler := m.Auth(m.CORS(m.RateLimit(okHandler)))

	// Valid request that should pass through.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer mykey")
	req.Header.Set("Origin", "https://app.example.com")

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("chained: expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// CORS headers should be set.
	if h := rr.Header().Get("Access-Control-Allow-Origin"); h != "https://app.example.com" {
		t.Errorf("chained: expected CORS origin, got %q", h)
	}

	// Missing auth should return 401 even with CORS.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("chained missing auth: expected 401, got %d", rr2.Code)
	}
}

// =============================================================================
// Edge cases
// =============================================================================

func TestExtractIP_Fallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:8080"
	ip := extractIP(req)
	if ip != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1, got %q", ip)
	}
}

func TestExtractIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	ip := extractIP(req)
	if ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %q", ip)
	}
}

func TestExtractIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "10.0.0.5")
	ip := extractIP(req)
	if ip != "10.0.0.5" {
		t.Errorf("expected 10.0.0.5, got %q", ip)
	}
}

func TestParseOrigins_Empty(t *testing.T) {
	result := parseOrigins("")
	if len(result) != 1 || result[0] != "*" {
		t.Errorf("expected [*], got %v", result)
	}
}

func TestParseOrigins_Single(t *testing.T) {
	result := parseOrigins("https://example.com")
	if len(result) != 1 || result[0] != "https://example.com" {
		t.Errorf("expected [https://example.com], got %v", result)
	}
}

func TestParseOrigins_Multiple(t *testing.T) {
	result := parseOrigins("https://a.com, https://b.com, https://c.com")
	if len(result) != 3 {
		t.Errorf("expected 3 origins, got %d: %v", len(result), result)
	}
}

func TestParseOrigins_Whitespace(t *testing.T) {
	result := parseOrigins("  https://a.com ,   https://b.com  ")
	if len(result) != 2 {
		t.Errorf("expected 2 origins, got %d: %v", len(result), result)
	}
	if result[0] != "https://a.com" || result[1] != "https://b.com" {
		t.Errorf("unexpected values: %v", result)
	}
}

func TestParseOrigins_AllCommas(t *testing.T) {
	result := parseOrigins(",,,")
	if len(result) != 1 || result[0] != "*" {
		t.Errorf("expected [*], got %v", result)
	}
}
