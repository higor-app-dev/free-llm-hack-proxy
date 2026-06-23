package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/higor/free-llm-hack-proxy/internal/browser"
	"github.com/higor/free-llm-hack-proxy/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("free-llm-hack-proxy — Go version")

	// Load browser pool configuration (defaults if no file found).
	cfg, err := config.LoadBrowserPoolConfig("")
	if err != nil {
		return fmt.Errorf("browser pool config: %w", err)
	}

	// Initialise the global browser pool.
	pool, err := browser.InitGlobalPool(cfg, nil)
	if err != nil {
		return fmt.Errorf("browser pool init: %w", err)
	}

	stats := pool.Stats()
	log.Printf("browser pool: %d browser(s) across %d provider(s)",
		stats.TotalBrowsers, len(stats.Providers))
	for name, ps := range stats.Providers {
		log.Printf("  %s: %d/%d slot(s) available", name, ps.AvailableSlots, ps.ConfiguredSlots)
	}

	// Demonstrate acquire/release for each provider.
	for name := range cfg.Providers {
		b := newBrowser(name)
		if b != nil {
			pool.Release(name, b)
		}
	}

	// --- Graceful shutdown ------------------------------------------------
	//
	// Wait for SIGINT or SIGTERM, then release all browsers and shut down
	// cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("received signal %v — shutting down browser pool", sig)

	pool.Close()
	pool.WaitDone()
	log.Print("browser pool closed cleanly — goodbye")

	return nil
}

// newBrowser acquires a browser from the pool for the given provider and
// prints its identity. Returns the browser so the caller can release it.
func newBrowser(provider string) browser.Browser {
	b, err := browser.GetGlobalPool().Acquire(provider)
	if err != nil {
		log.Printf("  acquire(%q): %v", provider, err)
		return nil
	}
	log.Printf("  acquire(%q): OK", provider)
	return b
}
