package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/higor/free-llm-hack-proxy/internal/api"
	"github.com/higor/free-llm-hack-proxy/internal/browser"
	"github.com/higor/free-llm-hack-proxy/internal/config"
	"github.com/higor/free-llm-hack-proxy/internal/providers/deepseek"
	"github.com/higor/free-llm-hack-proxy/internal/providers/mimo"
	"github.com/higor/free-llm-hack-proxy/internal/providers"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("free-llm-hack-proxy — Go version")

	// -----------------------------------------------------------------------
	// 1. Proxy configuration (env-based: HOST, PORT, LOG_LEVEL).
	// -----------------------------------------------------------------------
	proxyCfg := config.ProxyConfigFromEnv()
	log.Printf("proxy config: host=%s port=%d log_level=%s",
		proxyCfg.Host, proxyCfg.Port, proxyCfg.LogLevel)

	// -----------------------------------------------------------------------
	// 2. Provider registry — register every internal provider adapter.
	// -----------------------------------------------------------------------
	reg := providers.NewRegistry()

	deepseekProv := deepseek.New()
	if err := reg.Register(deepseekProv); err != nil {
		return fmt.Errorf("register deepseek: %w", err)
	}
	log.Printf("registered provider: %s (%s)", deepseekProv.Name(), deepseekProv.Description())

	mimoProv := mimo.New()
	if err := reg.Register(mimoProv); err != nil {
		return fmt.Errorf("register mimo: %w", err)
	}
	log.Printf("registered provider: %s (%s)", mimoProv.Name(), mimoProv.Description())

	log.Printf("provider registry: %d provider(s)", reg.Len())

	// -----------------------------------------------------------------------
	// 3. Browser pool configuration and initialisation.
	// -----------------------------------------------------------------------
	bpCfg, err := config.LoadBrowserPoolConfig("")
	if err != nil {
		return fmt.Errorf("browser pool config: %w", err)
	}

	pool, err := browser.InitGlobalPool(bpCfg, nil)
	if err != nil {
		return fmt.Errorf("browser pool init: %w", err)
	}

	stats := pool.Stats()
	log.Printf("browser pool: %d browser(s) across %d provider(s)",
		stats.TotalBrowsers, len(stats.Providers))
	for name, ps := range stats.Providers {
		log.Printf("  %s: %d/%d slot(s) available", name, ps.AvailableSlots, ps.ConfiguredSlots)
	}

	// -----------------------------------------------------------------------
	// 4. HTTP server — routes via api.NewHandler, wrapped with middleware.
	// -----------------------------------------------------------------------
	handler := api.NewHandler(reg)

	mwCfg := api.MiddlewareConfig{
		APIKey:         proxyCfg.APIKey,
		AllowedOrigins: proxyCfg.AllowedOrigins,
		RateLimitRPM:   proxyCfg.RateLimitRPM,
		RateLimitBurst: proxyCfg.RateLimitBurst,
	}
	mw, err := api.NewMiddleware(mwCfg)
	if err != nil {
		return fmt.Errorf("middleware: %w", err)
	}
	handler = mw.Auth(mw.CORS(mw.RateLimit(handler)))

	srv := &http.Server{
		Addr:         proxyCfg.Addr(),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // long-lived streaming responses
		IdleTimeout:  120 * time.Second,
	}

	// Start the server in a goroutine so we can listen for signals.
	errCh := make(chan error, 1)
	go func() {
		log.Printf("HTTP server listening on %s", proxyCfg.Addr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	// -----------------------------------------------------------------------
	// 5. Graceful shutdown — SIGINT / SIGTERM.
	// -----------------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received signal %v — shutting down", sig)
	case err := <-errCh:
		return err // server failed to start
	}

	// Create a context with a deadline so the server stops accepting new
	// requests and drains in-flight ones within the timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Print("draining HTTP server...")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown: %v", err)
	} else {
		log.Print("HTTP server closed cleanly")
	}

	log.Print("closing browser pool...")
	pool.Close()
	pool.WaitDone()
	log.Print("browser pool closed cleanly — goodbye")

	return nil
}
