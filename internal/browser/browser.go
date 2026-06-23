// Package browser provides browser automation via go-rod for provider login flows.
package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/higor/free-llm-hack-proxy/internal/config"
	"github.com/higor/free-llm-hack-proxy/internal/session"
)

// =============================================================================
// NewBrowser
// =============================================================================

// NewBrowser creates a single go-rod browser instance without going through the
// pool. It builds a default BrowserPoolConfig, creates a BrowserFactory via
// NewRodFactory, and delegates to the factory's NewBrowser.
//
// Pass nil opts to use the default headless launch configuration.
func NewBrowser(opts *config.BrowserLaunchOptions) (Browser, error) {
	cfg := config.DefaultBrowserPoolConfig()
	factory := NewRodFactory(cfg)

	ctx := context.Background()
	b, err := factory.NewBrowser(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("browser: launch: %w", err)
	}
	return b, nil
}

// =============================================================================
// SaveSession
// =============================================================================

// SaveSession persists cookies and localStorage for the given host by
// converting rod's NetworkCookie entries into session.CookieEntry and saving
// via session.Save.
//
// Pass an empty localStorage map (or nil) when there is no localStorage to
// persist.
func SaveSession(host string, cookies []*proto.NetworkCookie, localStorage map[string]string) error {
	if host == "" {
		return fmt.Errorf("browser: save session: host must not be empty")
	}

	entries := make([]session.CookieEntry, len(cookies))
	for i, c := range cookies {
		entries[i] = session.CookieEntry{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  int64(c.Expires),
			HttpOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: string(c.SameSite),
		}
	}

	if localStorage == nil {
		localStorage = make(map[string]string)
	}

	data := &session.SessionData{
		Cookies:      entries,
		LocalStorage: localStorage,
		Metadata: session.SessionMetadata{
			ProviderHost: host,
		},
	}

	if err := session.Save(data); err != nil {
		return fmt.Errorf("browser: save session for %q: %w", host, err)
	}
	return nil
}

// =============================================================================
// LoadSession
// =============================================================================

// LoadSession is a convenience wrapper around session.Load. It reads the
// persisted session for the given host and returns the full SessionData.
//
// Returns an error when no session exists for the host or the file is corrupt.
func LoadSession(host string) (*session.SessionData, error) {
	if host == "" {
		return nil, fmt.Errorf("browser: load session: host must not be empty")
	}

	data, err := session.Load(host)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	return data, nil
}

// =============================================================================
// GetPageWithSession
// =============================================================================

// GetPageWithSession creates a new page from the given Browser, loads the
// saved session for the host, restores cookies and localStorage into the page
// context, and navigates to https://<host>. Returns the page ready for
// interaction.
//
// The provided browser must be backed by a *rod.Browser (i.e. created via
// NewBrowser or the pool). Returns an error if no session exists for the host.
func GetPageWithSession(browser Browser, host string) (*rod.Page, error) {
	if host == "" {
		return nil, fmt.Errorf("browser: get page with session: host must not be empty")
	}

	// Type-assert to access rod.Browser methods for creating a page.
	rb, ok := browser.(*rodBrowser)
	if !ok {
		return nil, fmt.Errorf("browser: get page with session: browser is not a rod-backed browser")
	}

	// Create a new blank page.
	page, err := rb.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("browser: create page: %w", err)
	}

	// Load the saved session.
	s, err := session.Load(host)
	if err != nil {
		return nil, fmt.Errorf("browser: get page with session: load session for %q: %w", host, err)
	}

	// Convert session cookies to rod NetworkCookieParam and set them.
	params := make([]*proto.NetworkCookieParam, 0, len(s.Cookies))
	for _, c := range s.Cookies {
		p := &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
			SameSite: proto.NetworkCookieSameSite(c.SameSite),
		}
		// Only set Expires for persistent cookies (non-session cookies).
		// proto.NetworkCookieParam leaves Expires as the zero value when
		// omitted, which rod treats as a session cookie.
		if c.Expires > 0 {
			p.Expires = proto.TimeSinceEpoch(float64(c.Expires))
		}
		params = append(params, p)
	}

	if err := page.SetCookies(params); err != nil {
		return nil, fmt.Errorf("browser: set cookies for %q: %w", host, err)
	}

	// Restore localStorage entries via JavaScript evaluation.
	for key, value := range s.LocalStorage {
		k, _ := json.Marshal(key)
		v, _ := json.Marshal(value)
		js := fmt.Sprintf("localStorage.setItem(%s, %s)", string(k), string(v))
		if _, err := page.Evaluate(rod.Eval(js)); err != nil {
			return nil, fmt.Errorf("browser: set localStorage key %q for %q: %w", key, host, err)
		}
	}

	// Navigate to the host.
	targetURL := "https://" + host
	if err := page.Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("browser: navigate to %q: %w", targetURL, err)
	}

	return page, nil
}
