package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// =============================================================================
// Configuration
// =============================================================================

// MiddlewareConfig holds configuration for all HTTP middleware.
type MiddlewareConfig struct {
	APIKey          string
	AllowedOrigins  string
	AllowedMethods  string
	AllowedHeaders  string
	RateLimitRPM    int // requests per minute (0 = disabled)
	RateLimitBurst  int // burst capacity (0 = same as RPM)
}

// =============================================================================
// Token bucket (rate limiter internals)
// =============================================================================

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// =============================================================================
// Middleware
// =============================================================================

// Middleware holds the configured HTTP middleware functions.
type Middleware struct {
	apiKey         string
	allowedOrigins []string
	allowedMethods string
	allowedHeaders string
	rateLimitRate  float64 // tokens per second
	rateLimitBurst int
	rateEnabled    bool

	mu     sync.Mutex
	buckets map[string]*tokenBucket
}

// NewMiddleware creates a Middleware from the given config.
// It returns an error if the config is invalid.
func NewMiddleware(cfg MiddlewareConfig) (*Middleware, error) {
	m := &Middleware{
		apiKey:         cfg.APIKey,
		allowedOrigins: parseOrigins(cfg.AllowedOrigins),
		allowedMethods: defaultString(cfg.AllowedMethods, "GET, POST, OPTIONS"),
		allowedHeaders: defaultString(cfg.AllowedHeaders, "Content-Type, Authorization"),
		buckets:        make(map[string]*tokenBucket),
	}

	if cfg.RateLimitRPM > 0 {
		m.rateEnabled = true
		m.rateLimitRate = float64(cfg.RateLimitRPM) / 60.0
		m.rateLimitBurst = cfg.RateLimitBurst
		if m.rateLimitBurst <= 0 {
			m.rateLimitBurst = cfg.RateLimitRPM
		}
	}

	return m, nil
}

// parseOrigins splits a comma-separated origin string into a slice.
// Leading/trailing whitespace is trimmed from each entry.
func parseOrigins(s string) []string {
	if s == "" {
		return []string{"*"}
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return []string{"*"}
	}
	return result
}

// defaultString returns val if non-empty, otherwise def.
func defaultString(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

// =============================================================================
// Middleware methods (func(http.Handler) http.Handler)
// =============================================================================

// Auth returns HTTP middleware that checks the Authorization: Bearer <key>
// header. OPTIONS requests (CORS preflight) are always allowed through.
func (m *Middleware) Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow OPTIONS through for CORS preflight.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// No API key configured → allow all requests.
		if m.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		key := strings.TrimPrefix(auth, "Bearer ")
		if key != m.apiKey {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CORS returns HTTP middleware that sets CORS headers on every response and
// handles OPTIONS preflight requests by returning 204 No Content.
func (m *Middleware) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Determine the allowed origin value.
		allowOrigin := setCORSOrigin(m.allowedOrigins, origin)

		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", m.allowedMethods)
		w.Header().Set("Access-Control-Allow-Headers", m.allowedHeaders)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent) // 204
			return
		}

		next.ServeHTTP(w, r)
	})
}

// setCORSOrigin determines the Access-Control-Allow-Origin value.
// If the allowed origins contain "*", returns "*".
// If the request origin is in the allowed list, returns it (echo).
// Otherwise returns "" (no origin allowed).
func setCORSOrigin(allowed []string, origin string) string {
	for _, a := range allowed {
		if a == "*" {
			return "*"
		}
		if a == origin {
			return origin
		}
	}
	return ""
}

// RateLimit returns HTTP middleware that rate-limits requests per IP using a
// token bucket algorithm. When the rate limiter is disabled (RPM=0), this is
// a no-op that immediately calls the next handler.
func (m *Middleware) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.rateEnabled {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r)

		m.mu.Lock()
		bucket, exists := m.buckets[ip]
		now := time.Now()

		if !exists {
			bucket = &tokenBucket{
				tokens: float64(m.rateLimitBurst) - 1,
				last:   now,
			}
			m.buckets[ip] = bucket
			m.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}

		// Refill tokens based on elapsed time.
		elapsed := now.Sub(bucket.last).Seconds()
		bucket.tokens += elapsed * m.rateLimitRate
		if bucket.tokens > float64(m.rateLimitBurst) {
			bucket.tokens = float64(m.rateLimitBurst)
		}
		bucket.last = now

		if bucket.tokens < 1 {
			m.mu.Unlock()
			writeRateLimitError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		bucket.tokens--
		m.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// extractIP extracts the client IP from the request, preferring
// X-Forwarded-For or X-Real-IP headers, falling back to RemoteAddr.
func extractIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// Take the first IP in the chain.
		if idx := strings.IndexByte(fwd, ','); idx > 0 {
			return strings.TrimSpace(fwd[:idx])
		}
		return strings.TrimSpace(fwd)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	if idx := strings.LastIndexByte(addr, ':'); idx > 0 {
		return addr[:idx]
	}
	return addr
}

// =============================================================================
// Error helpers
// =============================================================================

// middlewareError mirrors the apiError shape used in api.go.
type middlewareError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error"`
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(middlewareError{struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	}{Message: msg, Type: "auth_error"}})
}

func writeRateLimitError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(middlewareError{struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	}{Message: msg, Type: "rate_limit_error"}})
}
