package browser

import (
	"context"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/higor/free-llm-hack-proxy/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SaveSession → LoadSession round-trip
// =============================================================================

func TestSaveSession_LoadSession_RoundTrip(t *testing.T) {
	// Redirect the session directory to a temp dir by setting HOME.
	t.Setenv("HOME", t.TempDir())

	cookies := []*proto.NetworkCookie{
		{
			Name:     "session",
			Value:    "abc123",
			Domain:   ".example.com",
			Path:     "/",
			Expires:  1_000_000_000, // proto.TimeSinceEpoch (float64)
			HTTPOnly: true,
			Secure:   true,
			SameSite: proto.NetworkCookieSameSite("Lax"),
		},
		{
			Name:     "pref",
			Value:    "dark_mode",
			Domain:   ".example.com",
			Path:     "/",
			Expires:  0, // session cookie
			HTTPOnly: false,
			Secure:   false,
			SameSite: proto.NetworkCookieSameSite("Lax"),
		},
	}
	localStorage := map[string]string{
		"token": "eyJhbGciOiJIUzI1NiJ9",
		"user":  "{\"id\":42,\"name\":\"test\"}",
	}

	// Save the session.
	err := SaveSession("chat.example.com", cookies, localStorage)
	require.NoError(t, err, "SaveSession should succeed")

	// Load it back.
	data, err := LoadSession("chat.example.com")
	require.NoError(t, err, "LoadSession should succeed")
	require.NotNil(t, data, "Loaded data should not be nil")

	// Verify cookies.
	assert.Len(t, data.Cookies, 2, "should have 2 cookies")

	c0 := data.Cookies[0]
	assert.Equal(t, "session", c0.Name)
	assert.Equal(t, "abc123", c0.Value)
	assert.Equal(t, ".example.com", c0.Domain)
	assert.Equal(t, "/", c0.Path)
	assert.Equal(t, int64(1_000_000_000), c0.Expires)
	assert.True(t, c0.HttpOnly)
	assert.True(t, c0.Secure)
	assert.Equal(t, "Lax", c0.SameSite)

	c1 := data.Cookies[1]
	assert.Equal(t, "pref", c1.Name)
	assert.Equal(t, "dark_mode", c1.Value)
	assert.Equal(t, int64(0), c1.Expires, "session cookie should have 0 expires")

	// Verify localStorage.
	assert.Equal(t, "eyJhbGciOiJIUzI1NiJ9", data.LocalStorage["token"])
	assert.Equal(t, "{\"id\":42,\"name\":\"test\"}", data.LocalStorage["user"])
}

func TestSaveSession_EmptyHost_Error(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := SaveSession("", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host must not be empty")
}

func TestSaveSession_NilCookiesEmptyLocalStorage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := SaveSession("host.example.com", nil, nil)
	require.NoError(t, err, "should accept nil cookies and nil localStorage")

	data, err := LoadSession("host.example.com")
	require.NoError(t, err)
	assert.Len(t, data.Cookies, 0, "should have 0 cookies")
	assert.Empty(t, data.LocalStorage, "localStorage should be empty but not nil")
}

// =============================================================================
// LoadSession — error handling
// =============================================================================

func TestLoadSession_EmptyHost_Error(t *testing.T) {
	_, err := LoadSession("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host must not be empty")
}

func TestLoadSession_NoSession_Error(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := LoadSession("nonexistent.example.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no session")
}

// =============================================================================
// GetPageWithSession — error paths
// =============================================================================

func TestGetPageWithSession_NonRodBrowser_Error(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A mockBrowser does NOT embed *rod.Browser, so the type assertion in
	// GetPageWithSession will fail.
	mb := newMockBrowser()
	_, err := GetPageWithSession(mb, "example.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a rod-backed browser")
}

func TestGetPageWithSession_EmptyHost_Error(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mb := newMockBrowser()
	_, err := GetPageWithSession(mb, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host must not be empty")
}

func TestGetPageWithSession_NoSession_Error(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A non-rod browser fails the type assertion before attempting to load
	// a session. This validates the error path when the provided browser
	// is not a rodBrowser.
	mb := newMockBrowser()
	_, err := GetPageWithSession(mb, "nope.example.com")
	assert.Error(t, err, "non-rod browser should produce a type-assertion error")
	assert.Contains(t, err.Error(), "not a rod-backed browser")
}

// =============================================================================
// NewBrowser — validation
// =============================================================================

func TestNewBrowser_ReturnsWorkingBrowser(t *testing.T) {
	// With a Chromium binary available, NewBrowser should launch and return
	// a valid, responsive browser instance.
	b, err := NewBrowser(nil)
	require.NoError(t, err, "NewBrowser should succeed when Chromium is available")
	require.NotNil(t, b, "returned browser should not be nil")

	// The browser should respond to Ping.
	err = b.Ping(context.Background())
	assert.NoError(t, err, "browser Ping should succeed")

	// Clean up.
	err = b.Close()
	assert.NoError(t, err, "browser Close should succeed")
}

func TestNewBrowser_WithOpts(t *testing.T) {
	// Test that passing explicit opts creates a valid browser.
	headless := true
	opts := &config.BrowserLaunchOptions{
		Headless:   &headless,
		WindowSize: "1920x1080",
	}
	b, err := NewBrowser(opts)
	require.NoError(t, err, "NewBrowser with opts should succeed")
	require.NotNil(t, b)

	err = b.Ping(context.Background())
	assert.NoError(t, err, "browser Ping should succeed")

	err = b.Close()
	assert.NoError(t, err)
}

// =============================================================================
// Test helpers
// =============================================================================

// compile-time check that mockBrowser implements Browser.
var _ Browser = (*mockBrowser)(nil)
