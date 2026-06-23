// Package browser provides browser automation via go-rod for provider login flows.
package browser

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/higor/free-llm-hack-proxy/internal/config"
)

// BrowserFactory creates new browser instances. The pool uses this to launch
// the configured number of browsers on startup and to re-launch on failures.
type BrowserFactory interface {
	// NewBrowser launches a new browser process and returns a connected instance.
	NewBrowser(ctx context.Context, opts *config.BrowserLaunchOptions) (Browser, error)
}

// rodBrowser wraps *rod.Browser and implements the Browser interface with a
// real Ping() that performs a lightweight liveness check via rod's Pages().
type rodBrowser struct {
	*rod.Browser
}

// Ping performs a lightweight liveness check by querying the browser's page
// list. This keeps the WebSocket connection alive and prevents idle timeouts.
func (b *rodBrowser) Ping(ctx context.Context) error {
	_, err := b.Pages()
	return err
}

// rodLauncherFactory is the production implementation of BrowserFactory that
// uses go-rod's launcher to start real Chromium processes.
type rodLauncherFactory struct {
	cfg *config.BrowserPoolConfig
}

// NewRodFactory creates a BrowserFactory backed by go-rod's launcher.
func NewRodFactory(cfg *config.BrowserPoolConfig) BrowserFactory {
	return &rodLauncherFactory{cfg: cfg}
}

// NewBrowser launches a Chromium browser with the given options and returns a
// connected Browser.
func (f *rodLauncherFactory) NewBrowser(ctx context.Context, opts *config.BrowserLaunchOptions) (Browser, error) {
	l := launcher.New()

	l = applyLaunchOpts(l, opts)

	// Launch the browser — this starts a Chromium child process and returns a
	// WebSocket URL for connecting.
	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	// Connect to the launched browser.
	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		// Browser launched but we couldn't connect; try to clean up.
		l.Kill()
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	return &rodBrowser{Browser: b}, nil
}

// applyLaunchOpts translates BrowserLaunchOptions to go-rod launcher flags.
func applyLaunchOpts(l *launcher.Launcher, opts *config.BrowserLaunchOptions) *launcher.Launcher {
	if opts == nil {
		return l
	}

	if opts.Headless != nil {
		l.Headless(*opts.Headless)
	}

	if opts.Proxy != "" {
		l.Proxy(opts.Proxy)
	}

	if opts.UserDataDir != "" {
		l.UserDataDir(opts.UserDataDir)
	}

	if opts.WindowSize != "" {
		parts := strings.SplitN(opts.WindowSize, "x", 2)
		if len(parts) == 2 {
			w := strings.TrimSpace(parts[0])
			h := strings.TrimSpace(parts[1])
			if _, errW := strconv.Atoi(w); errW == nil {
				if _, errH := strconv.Atoi(h); errH == nil {
					l.Set(flags.Flag("window-size"), opts.WindowSize)
				}
			}
		}
	}

	for _, arg := range opts.ExtraArgs {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			// Strip leading dashes to convert "--no-sandbox" → "no-sandbox"
			flagName := strings.TrimLeft(arg, "-")
			l.Set(flags.Flag(flagName))
		}
	}

	return l
}
