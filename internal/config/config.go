// Package config loads and validates configuration from YAML files, env vars,
// and default values.
//
// The package supports two configuration domains:
//   - Proxy: host, port, log level for the HTTP server itself. See ProxyConfig.
//   - Browser pool: defines go-rod instance management per LLM provider
//     (slots, heartbeat interval, launch options). See BrowserPoolConfig.
//
// Usage:
//
//	cfg, err := config.LoadBrowserPoolConfig("/path/to/browser_pool.yaml")
//	if err != nil { ... }
//	fmt.Println(cfg.Providers["deepseek"].Slots)
//
//	proxyCfg := config.DefaultProxyConfig()
//	fmt.Println(proxyCfg.Port) // 8080
package config

import (
	"fmt"
	"os"
	"strconv"
)

// =============================================================================
// ProxyConfig
// =============================================================================

// ProxyConfig holds the HTTP server configuration.
type ProxyConfig struct {
	// Host is the bind address (default: "127.0.0.1").
	Host string

	// Port is the listen port (default: 8080).
	Port int

	// LogLevel controls verbosity (default: "info").
	LogLevel string

	// APIKey is an optional bearer token for request authentication.
	// When empty, authentication is disabled.
	APIKey string

	// AllowedOrigins is a comma-separated list of CORS origins (default: "*").
	AllowedOrigins string

	// RateLimitRPM is the max requests per minute per IP (0 = disabled).
	RateLimitRPM int

	// RateLimitBurst is the burst capacity (0 = same as RateLimitRPM).
	RateLimitBurst int
}

// DefaultProxyConfig returns sensible defaults for the HTTP proxy server.
func DefaultProxyConfig() *ProxyConfig {
	return &ProxyConfig{
		Host:     "127.0.0.1",
		Port:     8080,
		LogLevel: "info",
	}
}

// ProxyConfigFromEnv reads proxy configuration from environment variables.
// Values not set in env fall back to defaults.
func ProxyConfigFromEnv() *ProxyConfig {
	cfg := DefaultProxyConfig()

	if h := os.Getenv("HOST"); h != "" {
		cfg.Host = h
	}
	if p := os.Getenv("PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		}
	}
	if ll := os.Getenv("LOG_LEVEL"); ll != "" {
		cfg.LogLevel = ll
	}
	if ak := os.Getenv("PROXY_API_KEY"); ak != "" {
		cfg.APIKey = ak
	}
	if ao := os.Getenv("PROXY_ALLOWED_ORIGINS"); ao != "" {
		cfg.AllowedOrigins = ao
	}
	if rl := os.Getenv("PROXY_RATE_LIMIT_RPM"); rl != "" {
		if v, err := strconv.Atoi(rl); err == nil && v >= 0 {
			cfg.RateLimitRPM = v
		}
	}
	if rb := os.Getenv("PROXY_RATE_LIMIT_BURST"); rb != "" {
		if v, err := strconv.Atoi(rb); err == nil && v >= 0 {
			cfg.RateLimitBurst = v
		}
	}
	return cfg
}

// Addr returns the host:port string to pass to http.ListenAndServe.
func (c *ProxyConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
