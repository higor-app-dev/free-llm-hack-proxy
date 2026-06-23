// Package session manages persisted browser session state for LLM provider
// authentication. Sessions store the cookies and localStorage that a browser
// context accumulated during an interactive login, so they can be restored
// in headless browsing during subsequent chat requests.
//
// Each provider saves its own session file at:
//
//	~/.llm-proxy/sessions/<provider-host>.json
//
// where <provider-host> is the domain name of the provider's web interface
// (e.g. "chat.deepseek.com", "aistudio.xiaomimimo.com").
//
// File format (JSON)
// ====================
//
//	{
//	  "cookies": [
//	    {
//	      "name": "cloud_session",
//	      "value": "abc123...",
//	      "domain": ".deepseek.com",
//	      "path": "/",
//	      "expires": 1760731860,
//	      "httpOnly": true,
//	      "secure": true,
//	      "sameSite": "Lax"
//	    }
//	  ],
//	  "localStorage": {
//	    "token": "eyJhbGciOi...",
//	    "user": "{\"id\": 42, ...}"
//	  },
//	  "metadata": {
//	    "provider": "deepseek",
//	    "provider_host": "chat.deepseek.com",
//	    "created_at": 1718563200,
//	    "expires_at": 1760731860,
//	    "expires_after_seconds": 86400,
//	    "refresh_token": "rt_abc123..."
//	  }
//	}
package session

import (
	"time"
)

// =============================================================================
// CookieEntry
// =============================================================================

// CookieEntry models a single HTTP cookie as stored by the Playwright /
// go-rod storageState API. Fields mirror the standard cookie attributes
// recognised by Chromium.
type CookieEntry struct {
	// Name is the cookie key (e.g. "cloud_session", "sessionid").
	Name string `json:"name"`

	// Value is the cookie value — sensitive, store at rest with care.
	Value string `json:"value"`

	// Domain restricts the cookie to matching domains/subdomains.
	// May include a leading dot (e.g. ".deepseek.com").
	Domain string `json:"domain"`

	// Path restricts the cookie to matching URL paths (default "/").
	Path string `json:"path"`

	// Expires is the cookie's expiry timestamp as a Unix epoch (seconds).
	// A value of -1 or 0 means the cookie is a session cookie (expires on
	// browser close).
	Expires int64 `json:"expires"`

	// HttpOnly restricts access from JavaScript (document.cookie).
	HttpOnly bool `json:"httpOnly"`

	// Secure restricts the cookie to HTTPS connections only.
	Secure bool `json:"secure"`

	// SameSite controls cross-site request behaviour.
	// Typical values: "Lax", "Strict", "None".
	SameSite string `json:"sameSite,omitempty"`
}

// IsSessionCookie returns true when the cookie has no explicit expiry
// (session cookie — should not be persisted long-term).
func (c *CookieEntry) IsSessionCookie() bool {
	return c.Expires <= 0
}

// IsExpired returns true when the cookie's expiry timestamp is in the past.
func (c *CookieEntry) IsExpired() bool {
	if c.IsSessionCookie() {
		return false // session cookies have no fixed expiry
	}
	return time.Unix(c.Expires, 0).Before(time.Now())
}

// =============================================================================
// SessionMetadata
// =============================================================================

// SessionMetadata stores provenance and lifecycle information about a
// captured browser session.
type SessionMetadata struct {
	// Provider is the short name of the LLM provider (e.g. "deepseek", "mimo").
	Provider string `json:"provider"`

	// ProviderHost is the web domain where the session was captured
	// (e.g. "chat.deepseek.com"). Used as the session file key.
	ProviderHost string `json:"provider_host"`

	// CreatedAt is the Unix epoch (seconds) when the session was first
	// captured and saved.
	CreatedAt int64 `json:"created_at"`

	// ExpiresAt is the Unix epoch (seconds) at which the session is
	// considered expired and needs re-login. 0 means "no explicit expiry"
	// (check cookies individually).
	ExpiresAt int64 `json:"expires_at,omitempty"`

	// ExpiresAfterSeconds is an alternative to ExpiresAt: the session is
	// considered expired when (time.Now().Unix() - CreatedAt) exceeds this
	// value. 0 means unused.
	ExpiresAfterSeconds int64 `json:"expires_after_seconds,omitempty"`

	// RefreshToken is an optional OAuth-style token that can be used to
	// refresh the session without a full interactive login. Not all providers
	// support this.
	RefreshToken string `json:"refresh_token,omitempty"`

	// ProviderVersion is an optional semver string identifying the provider
	// implementation that captured this session. Useful for migration.
	ProviderVersion string `json:"provider_version,omitempty"`
}

// IsExpired returns true when the metadata indicates the session has expired,
// either by explicit ExpiresAt or by ExpiresAfterSeconds.
func (m *SessionMetadata) IsExpired() bool {
	now := time.Now().Unix()

	// Check explicit expiry timestamp.
	if m.ExpiresAt > 0 && now >= m.ExpiresAt {
		return true
	}

	// Check relative expiry from creation time.
	if m.ExpiresAfterSeconds > 0 && m.CreatedAt > 0 {
		if now-m.CreatedAt >= m.ExpiresAfterSeconds {
			return true
		}
	}

	return false
}

// =============================================================================
// SessionData
// =============================================================================

// SessionData is the root structure persisted to disk. It contains the
// cookies and localStorage that together form a browsable authenticated
// session for an LLM provider's web interface.
type SessionData struct {
	// Cookies is the list of HTTP cookies to restore into the browser
	// context. These are the cookies stored by Chromium after a successful
	// login.
	Cookies []CookieEntry `json:"cookies"`

	// LocalStorage is a key-value map of browser localStorage entries to
	// restore. Each provider may store auth tokens, user profile data, and
	// UI preferences here.
	LocalStorage map[string]string `json:"localStorage"`

	// Metadata carries provenance and lifecycle information for the session.
	Metadata SessionMetadata `json:"metadata"`
}

// IsExpired checks whether the session as a whole is expired by examining
// both the metadata-level expiry and the cookie-level expiry.
//
// A session is expired when:
//  1. The metadata says so (ExpiresAt or ExpiresAfterSeconds), OR
//  2. ALL non-session cookies are individually expired.
//
// A session with no cookies and no metadata expiry is not considered expired
// (it simply has no usable auth material — a separate validation concern).
func (s *SessionData) IsExpired() bool {
	// Metadata-level expiry is authoritative.
	if s.Metadata.IsExpired() {
		return true
	}

	// Check cookie-level expiry: if every named cookie is expired, treat
	// the session as expired. Session cookies (expires <= 0) are skipped
	// because they carry no expiry date — they're ephemeral.
	persistentCount := 0
	expiredCount := 0
	for i := range s.Cookies {
		if s.Cookies[i].IsSessionCookie() {
			continue // session cookies never expire on their own
		}
		persistentCount++
		if s.Cookies[i].IsExpired() {
			expiredCount++
		}
	}

	// If there are persistent cookies and all have expired, the session
	// is unusable.
	if persistentCount > 0 && expiredCount == persistentCount {
		return true
	}

	return false
}

// AuthCookieCount returns the number of cookies whose name matches one of
// the given auth indicator names. Used to validate that a session actually
// contains auth material from a login attempt.
func (s *SessionData) AuthCookieCount(indicatorNames map[string]bool) int {
	count := 0
	for i := range s.Cookies {
		if indicatorNames[s.Cookies[i].Name] {
			count++
		}
	}
	return count
}

// AuthLocalStorageCount returns the number of localStorage keys whose name
// matches one of the given auth indicator keys.
func (s *SessionData) AuthLocalStorageCount(indicatorKeys map[string]bool) int {
	count := 0
	for k := range s.LocalStorage {
		if indicatorKeys[k] {
			count++
		}
	}
	return count
}

// CookieCount is a convenience accessor.
func (s *SessionData) CookieCount() int {
	return len(s.Cookies)
}

// LocalStorageKeyCount is a convenience accessor.
func (s *SessionData) LocalStorageKeyCount() int {
	return len(s.LocalStorage)
}
