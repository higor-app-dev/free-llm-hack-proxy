// Package session manages persisted browser session state for LLM provider
// authentication. Sessions store cookies + localStorage captured during an
// interactive login, serialised as JSON at ~/.llm-proxy/sessions/<host>.json.
//
// Usage
// -----
//
//	// Save a newly captured session.
//	s := &session.SessionData{
//	    Cookies: []session.CookieEntry{{Name: "cloud_session", Value: "abc123", ...}},
//	    LocalStorage: map[string]string{"token": "eyJ..."},
//	    Metadata: session.SessionMetadata{
//	        Provider:     "deepseek",
//	        ProviderHost: "chat.deepseek.com",
//	        CreatedAt:    time.Now().Unix(),
//	    },
//	}
//	if err := session.Save(s); err != nil { ... }
//
//	// Load and check validity.
//	s, err := session.Load("chat.deepseek.com")
//	if err != nil { ... }
//	if s.IsExpired() { /* re-login needed */ }
//
//	// Remove stale session.
//	if err := session.Delete("chat.deepseek.com"); err != nil { ... }
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// =============================================================================
// Path resolution
// =============================================================================

const (
	// SessionsDirName is the directory name under $HOME where session files
	// are stored. The full path is ~/.llm-proxy/sessions/.
	SessionsDirName = ".llm-proxy/sessions"
)

// SessionsDir returns the absolute path to the sessions directory, creating
// it (and parents) if they don't already exist.
//
// The directory is always:
//
//	$HOME/.llm-proxy/sessions/
func SessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: resolve home dir: %w", err)
	}

	dir := filepath.Join(home, SessionsDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("session: create sessions dir %q: %w", dir, err)
	}

	return dir, nil
}

// SessionPath returns the full path to a provider's session JSON file:
//
//	~/.llm-proxy/sessions/<providerHost>.json
//
// The parent directory is created if it doesn't exist.
func SessionPath(providerHost string) (string, error) {
	if providerHost == "" {
		return "", errors.New("session: provider host must not be empty")
	}

	dir, err := SessionsDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, providerHost+".json"), nil
}

// =============================================================================
// Save
// =============================================================================

// Save persists a SessionData to disk at ~/.llm-proxy/sessions/<host>.json.
//
// The file is written atomically: content is written to a temporary file
// first, then renamed into place. This prevents partial reads from concurrent
// loaders.
func Save(s *SessionData) error {
	if s == nil {
		return errors.New("session: cannot save nil SessionData")
	}

	path, err := SessionPath(s.Metadata.ProviderHost)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal %q: %w", path, err)
	}

	// Atomic write: temp file → rename.
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("session: write temp %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Clean up temp file on rename failure.
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("session: rename %q → %q: %w", tmpPath, path, err)
	}

	return nil
}

// =============================================================================
// Load
// =============================================================================

// Load reads a previously saved session for the given provider host.
// Returns a non-nil error if the file does not exist or cannot be parsed.
func Load(providerHost string) (*SessionData, error) {
	path, err := SessionPath(providerHost)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session: no session for %q (file %q not found)", providerHost, path)
		}
		return nil, fmt.Errorf("session: read %q: %w", path, err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("session: file %q is empty", path)
	}

	s := &SessionData{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("session: parse %q: %w", path, err)
	}

	return s, nil
}

// =============================================================================
// Exists
// =============================================================================

// Exists returns true when a session file exists on disk for the given
// provider host. It does not check whether the session data is valid or
// expired — use Load + IsExpired for that.
func Exists(providerHost string) (bool, error) {
	path, err := SessionPath(providerHost)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("session: stat %q: %w", path, err)
}

// =============================================================================
// Delete
// =============================================================================

// Delete removes the session file for the given provider host from disk.
// It is not an error if the file does not exist.
func Delete(providerHost string) error {
	path, err := SessionPath(providerHost)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil // idempotent — already gone
		}
		return fmt.Errorf("session: remove %q: %w", path, err)
	}

	return nil
}

// =============================================================================
// List
// =============================================================================

// List returns all provider hosts that currently have session files on disk.
// Each entry is a host string (e.g. "chat.deepseek.com") — the caller can
// pass it to Load to retrieve the full session data.
func List() ([]string, error) {
	dir, err := SessionsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("session: list %q: %w", dir, err)
	}

	var hosts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		hosts = append(hosts, name[:len(name)-len(".json")])
	}

	return hosts, nil
}

// =============================================================================
// Convenience: IsExpired helper
// =============================================================================

// IsExpired is a convenience wrapper around Load + SessionData.IsExpired.
// Returns true when the session file does not exist, cannot be parsed, or
// the embedded expiry indicates the session is stale.
//
// This is the fast validation check used by providers before attempting
// a chat request. It makes no network calls — it only inspects the JSON.
func IsExpired(providerHost string) bool {
	s, err := Load(providerHost)
	if err != nil {
		return true // missing or corrupt = expired
	}
	return s.IsExpired()
}

// =============================================================================
// ValidationResult
// =============================================================================

// ValidationResult describes the outcome of a session validation check.
//
// Valid is true when the session is usable. When false, Reason contains a
// human-readable summary and Details lists every issue found during
// validation so callers can surface granular diagnostics.
type ValidationResult struct {
	// Valid is true when the session is fit for use.
	Valid bool `json:"valid"`

	// Reason is a short summary of why the session is invalid, or empty
	// when Valid is true.
	Reason string `json:"reason,omitempty"`

	// Details lists every specific issue found during validation.
	// Each entry pinpoints a field and explains the problem.
	Details []string `json:"details,omitempty"`
}

// =============================================================================
// Validate
// =============================================================================

// Validate checks whether a loaded SessionData is still valid for use.
//
// Validation runs the following checks:
//  1. Nil input — a nil session is always invalid.
//  2. Metadata-level expiry — if ExpiresAt is set and in the past, or
//     ExpiresAfterSeconds has elapsed since CreatedAt, the session is expired.
//  3. Individual cookie expiry — if any persistent cookie (Expires > 0) has
//     an expiry in the past, the session is invalid.
//
// Every invalid finding is logged via log.Printf with the provider host
// and the specific field that failed, so operators can trace why a session
// was rejected.
func Validate(s *SessionData) *ValidationResult {
	if s == nil {
		log.Print("session validate: nil SessionData — invalid")
		return &ValidationResult{
			Valid:   false,
			Reason:  "session data is nil",
			Details: []string{"SessionData pointer is nil"},
		}
	}

	host := s.Metadata.ProviderHost
	var details []string

	// --- 1. Metadata-level expiry ---

	if s.Metadata.ExpiresAt > 0 {
		now := time.Now().Unix()
		if now >= s.Metadata.ExpiresAt {
			msg := fmt.Sprintf("metadata.expires_at (%d) is in the past (now=%d)", s.Metadata.ExpiresAt, now)
			log.Printf("session validate: provider=%q host=%q %s", s.Metadata.Provider, host, msg)
			details = append(details, msg)
		}
	}

	if s.Metadata.ExpiresAfterSeconds > 0 && s.Metadata.CreatedAt > 0 {
		now := time.Now().Unix()
		elapsed := now - s.Metadata.CreatedAt
		if elapsed >= s.Metadata.ExpiresAfterSeconds {
			msg := fmt.Sprintf("metadata.expires_after_seconds (%ds) elapsed (%ds since created_at=%d)",
				s.Metadata.ExpiresAfterSeconds, elapsed, s.Metadata.CreatedAt)
			log.Printf("session validate: provider=%q host=%q %s", s.Metadata.Provider, host, msg)
			details = append(details, msg)
		}
	}

	// --- 2. Individual cookie expiry ---

	for i := range s.Cookies {
		c := &s.Cookies[i]
		if c.IsSessionCookie() {
			continue // session cookies have no fixed expiry — skip
		}
		if c.IsExpired() {
			msg := fmt.Sprintf("cookie[%d].%q expired at %d (domain=%s path=%s)",
				i, c.Name, c.Expires, c.Domain, c.Path)
			log.Printf("session validate: provider=%q host=%q %s", s.Metadata.Provider, host, msg)
			details = append(details, msg)
		}
	}

	if len(details) > 0 {
		var b strings.Builder
		b.WriteString("session invalid: ")
		b.WriteString(fmt.Sprintf("%d issue(s) found", len(details)))
		return &ValidationResult{
			Valid:   false,
			Reason:  b.String(),
			Details: details,
		}
	}

	return &ValidationResult{
		Valid:   true,
		Reason:  "",
		Details: nil,
	}
}

// =============================================================================
// Errors
// =============================================================================

var (
	// ErrNoRefreshToken is returned by Refresh when the session has no
	// refresh_token field set in its metadata, meaning the provider does not
	// support token-based session refresh.
	ErrNoRefreshToken = errors.New("session: session has no refresh_token")
)

// =============================================================================
// RefreshStrategy interface
// =============================================================================

// RefreshStrategy defines how a provider refreshes an expiring browser
// session. Each provider adapter implements this interface with its own
// HTTP calls (POST to a token endpoint, etc.).
//
// Implementations should be stateless — the session package owns persistence
// and error handling around the refresh.
type RefreshStrategy interface {
	// Refresh uses the given refreshToken to obtain new authentication
	// material. Returns a fully populated SessionData with fresh cookies,
	// updated localStorage, and updated metadata (new CreatedAt, ExpiresAt,
	// and optionally a new refresh_token if the server rotates tokens).
	Refresh(refreshToken string) (*SessionData, error)

	// Name returns a human-readable identifier for this strategy, used
	// in log lines and error messages.
	Name() string
}

// =============================================================================
// Refresh
// =============================================================================

// Refresh loads the session for the given provider host, uses the strategy
// to obtain fresh credentials, and persists the updated session atomically.
//
// If the session has no refresh_token, it returns ErrNoRefreshToken.
// If the strategy returns an error, the original session file on disk is
// left completely unchanged.
//
// Example usage:
//
//	updated, err := session.Refresh("chat.deepseek.com", myStrategy)
//	if errors.Is(err, session.ErrNoRefreshToken) {
//	    // fall back to interactive login
//	} else if err != nil {
//	    // refresh failed — old session still on disk
//	}
//	// updated holds the new session data
func Refresh(providerHost string, strategy RefreshStrategy) (*SessionData, error) {
	if strategy == nil {
		return nil, errors.New("session refresh: strategy must not be nil")
	}

	// 1. Load existing session from disk.
	old, err := Load(providerHost)
	if err != nil {
		return nil, fmt.Errorf("session refresh: load %q: %w", providerHost, err)
	}

	// 2. Check for a refresh token.
	if old.Metadata.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	log.Printf("session refresh: refreshing session for %q via strategy %q", providerHost, strategy.Name())

	// 3. Invoke the strategy. If this fails we return immediately — no
	//    file has been written, so the old session is preserved.
	updated, err := strategy.Refresh(old.Metadata.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("session refresh: strategy %q failed: %w", strategy.Name(), err)
	}
	if updated == nil {
		return nil, fmt.Errorf("session refresh: strategy %q returned nil SessionData without error", strategy.Name())
	}

	// 4. Preserve the identity fields from the original. The provider
	//    host is the file key and must never change.
	updated.Metadata.ProviderHost = providerHost

	// 5. Write the updated session atomically.
	if err := Save(updated); err != nil {
		return nil, fmt.Errorf("session refresh: save updated session for %q: %w", providerHost, err)
	}

	log.Printf("session refresh: session for %q refreshed successfully", providerHost)
	return updated, nil
}

// =============================================================================
// NeedsRefresh — proactive refresh check
// =============================================================================

// NeedsRefresh reports whether the session is close enough to its expiry
// that a proactive refresh is worthwhile. The gracePeriod defines the
// look-ahead window (e.g. 5 minutes, 1 hour).
//
// All conditions must be met:
//  1. The session has a non-empty refresh_token.
//  2. At least one expiry field is set (ExpiresAt or ExpiresAfterSeconds).
//  3. The current time plus gracePeriod is at or past the deadline.
//
// A nil session or one with no expiry fields returns false.
func NeedsRefresh(s *SessionData, gracePeriod time.Duration) bool {
	if s == nil {
		return false
	}
	if s.Metadata.RefreshToken == "" {
		return false
	}

	now := time.Now()

	// Check explicit ExpiresAt.
	if s.Metadata.ExpiresAt > 0 {
		deadline := time.Unix(s.Metadata.ExpiresAt, 0)
		if now.Add(gracePeriod).After(deadline) || now.Add(gracePeriod).Equal(deadline) {
			return true
		}
	}

	// Check relative expiry (ExpiresAfterSeconds from CreatedAt).
	if s.Metadata.ExpiresAfterSeconds > 0 && s.Metadata.CreatedAt > 0 {
		deadline := time.Unix(s.Metadata.CreatedAt+s.Metadata.ExpiresAfterSeconds, 0)
		if now.Add(gracePeriod).After(deadline) || now.Add(gracePeriod).Equal(deadline) {
			return true
		}
	}

	return false
}
