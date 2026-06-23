// Package mimo implements the MiMo provider adapter.
//
// This file contains unit tests for the MiMo provider. Each test creates a
// temporary HOME directory so session files are written and read in isolation.
package mimo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/higor/free-llm-hack-proxy/internal/providers"
	"github.com/higor/free-llm-hack-proxy/internal/session"
)

// =============================================================================
// Tests: buildPromptText
// =============================================================================

func TestBuildPromptText_SingleMessage(t *testing.T) {
	msg := []providers.ChatMessage{{Role: "user", Content: "hello world"}}
	got := buildPromptText(msg)
	if got != "hello world" {
		t.Errorf("buildPromptText(single) = %q, want %q", got, "hello world")
	}
}

func TestBuildPromptText_MultiTurn(t *testing.T) {
	msgs := []providers.ChatMessage{
		{Role: "user", Content: "what is 2+2"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "what is 3+3"},
	}
	got := buildPromptText(msgs)
	want := "user: what is 2+2\nassistant: 4\nuser: what is 3+3"
	if got != want {
		t.Errorf("buildPromptText(multi) = %q, want %q", got, want)
	}
}

func TestBuildPromptText_Empty(t *testing.T) {
	if got := buildPromptText(nil); got != "" {
		t.Errorf("buildPromptText(nil) = %q, want empty", got)
	}
	if got := buildPromptText([]providers.ChatMessage{}); got != "" {
		t.Errorf("buildPromptText(empty) = %q, want empty", got)
	}
}

func TestBuildPromptText_SystemMessage(t *testing.T) {
	msgs := []providers.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "hi"},
	}
	got := buildPromptText(msgs)
	want := "system: You are a helpful assistant.\nuser: hi"
	if got != want {
		t.Errorf("buildPromptText(system) = %q, want %q", got, want)
	}
}

// =============================================================================
// Tests: cookiesToParams
// =============================================================================

func TestCookiesToParams_Empty(t *testing.T) {
	params := cookiesToParams(nil)
	if len(params) != 0 {
		t.Errorf("cookiesToParams(nil) = %d, want 0", len(params))
	}

	params = cookiesToParams([]session.CookieEntry{})
	if len(params) != 0 {
		t.Errorf("cookiesToParams(empty) = %d, want 0", len(params))
	}
}

func TestCookiesToParams_ConvertsAllFields(t *testing.T) {
	cookies := []session.CookieEntry{
		{
			Name:     "session",
			Value:    "abc123",
			Domain:   ".xiaomimimo.com",
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: "Lax",
			Expires:  9999999999,
		},
	}

	params := cookiesToParams(cookies)
	if len(params) != 1 {
		t.Fatalf("cookiesToParams = %d items, want 1", len(params))
	}

	p := params[0]
	if p.Name != "session" {
		t.Errorf("Name = %q, want %q", p.Name, "session")
	}
	if p.Value != "abc123" {
		t.Errorf("Value = %q, want %q", p.Value, "abc123")
	}
	if p.Domain != ".xiaomimimo.com" {
		t.Errorf("Domain = %q, want %q", p.Domain, ".xiaomimimo.com")
	}
	if p.Path != "/" {
		t.Errorf("Path = %q, want %q", p.Path, "/")
	}
	if !p.Secure {
		t.Errorf("Secure = false, want true")
	}
	if !p.HTTPOnly {
		t.Errorf("HTTPOnly = false, want true")
	}
	if p.SameSite != proto.NetworkCookieSameSite("Lax") {
		t.Errorf("SameSite = %q, want %q", p.SameSite, "Lax")
	}
	if p.Expires != proto.TimeSinceEpoch(9999999999) {
		t.Errorf("Expires mismatch")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// newSession saves a SessionData for aistudio.xiaomimimo.com into the session
// directory under the given temporary HOME. It returns the provider so callers
// can call IsSessionValid() without creating a separate instance.
func newSession(t *testing.T, s *session.SessionData) {
	t.Helper()
	s.Metadata.Provider = "mimo"
	s.Metadata.ProviderHost = ProviderHost
	if err := session.Save(s); err != nil {
		t.Fatalf("session.Save: %v", err)
	}
}

// farFuture is a Unix timestamp far enough in the future that it will never
// expire during a test run.
var farFuture = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

// =============================================================================
// Tests: IsSessionValid
// =============================================================================

func TestIsSessionValid_NoSession(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (no session saved)")
	}
}

func TestIsSessionValid_ValidWithSessionCookie(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "session_id",
				Value:   "abc123",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: 0, // session cookie — no explicit expiry
				Secure:  true,
			},
		},
		LocalStorage: map[string]string{"token": "eyJhbG...NiJ9"},
		Metadata: session.SessionMetadata{
			CreatedAt: time.Now().Unix(),
		},
	})

	p := New()
	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false, want true (valid session cookie)")
	}
}

func TestIsSessionValid_ValidWithPersistentCookie(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "cloud_session",
				Value:   "valid-token",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: farFuture,
				Secure:  true,
			},
		},
		Metadata: session.SessionMetadata{
			CreatedAt: time.Now().Unix(),
		},
	})

	p := New()
	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false, want true (valid persistent cookie)")
	}
}

func TestIsSessionValid_ExpiredByMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "cloud_session",
				Value:   "stale-token",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: farFuture,
				Secure:  true,
			},
		},
		Metadata: session.SessionMetadata{
			CreatedAt: time.Now().Unix(),
			ExpiresAt: time.Now().Unix() - 3600, // expired 1 hour ago
		},
	})

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (metadata ExpiresAt in the past)")
	}
}

func TestIsSessionValid_ExpiredByExpiresAfter(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "cloud_session",
				Value:   "stale-token",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: farFuture,
				Secure:  true,
			},
		},
		Metadata: session.SessionMetadata{
			CreatedAt:           time.Now().Unix() - 7200, // created 2h ago
			ExpiresAfterSeconds: 3600,                     // expires after 1h
		},
	})

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (ExpiresAfterSeconds elapsed)")
	}
}

func TestIsSessionValid_ExpiredByAllCookies(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "old_session",
				Value:   "expired1",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: 1_000_000, // unix timestamp in year 2001 — long expired
			},
			{
				Name:    "another_session",
				Value:   "expired2",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: 2_000_000, // also expired
			},
		},
		Metadata: session.SessionMetadata{
			CreatedAt: time.Now().Unix(),
		},
	})

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (all cookies expired)")
	}
}

func TestIsSessionValid_MixedCookieTypes(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Validate checks that NO persistent cookie is expired. Session cookies
	// (Expires <= 0) are skipped. This session has a session cookie and a
	// valid persistent cookie — should pass.
	newSession(t, &session.SessionData{
		Cookies: []session.CookieEntry{
			{
				Name:    "session_id",
				Value:   "ephemeral",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: 0, // session cookie — no expiry, skipped by Validate
			},
			{
				Name:    "persistent_token",
				Value:   "still-good",
				Domain:  ".xiaomimimo.com",
				Path:    "/",
				Expires: farFuture, // valid — not expired
				Secure:  true,
			},
		},
		Metadata: session.SessionMetadata{
			CreatedAt: time.Now().Unix(),
		},
	})

	p := New()
	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false, want true (session + valid persistent cookie)")
	}
}

func TestIsSessionValid_CorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write invalid JSON to the session file path.
	sessionDir := filepath.Join(tmpDir, ".llm-proxy", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(sessionDir, ProviderHost+".json")
	if err := os.WriteFile(corruptPath, []byte("this is not json"), 0600); err != nil {
		t.Fatal(err)
	}

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (corrupt file)")
	}
}

func TestIsSessionValid_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write an empty file.
	sessionDir := filepath.Join(tmpDir, ".llm-proxy", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(sessionDir, ProviderHost+".json")
	if err := os.WriteFile(emptyPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	p := New()
	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (empty file)")
	}
}

// =============================================================================
// Tests: Login
// =============================================================================

// TestLogin_InvalidURL verifies that Login returns an error when given a
// context with an invalid BaseURL. The context deadline has already passed,
// so Login returns immediately without attempting to launch a browser.
func TestLogin_InvalidURL(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()

	// Create an already-expired context — Login checks the deadline first
	// and returns immediately with ErrTimeout before any browser interaction.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	err := p.Login(ctx, providers.AuthConfig{
		BaseURL: "://invalid-url", // would be invalid if reached, but we never get there
	})
	if err == nil {
		t.Fatal("Login() with expired context and invalid URL expected error, got nil")
	}

	// Verify the error is a timeout error from the early-exit code path.
	var pe *providers.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Login() error type = %T, want *providers.ProviderError", err)
	}
	if pe.Code != 504 {
		t.Errorf("Login() error code = %d, want %d (timeout)", pe.Code, 504)
	}
}

// TestLogin_ContextTimeout verifies that Login returns a timeout error when
// the context deadline has already expired.
func TestLogin_ContextTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()

	// Create an already-expired context.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	err := p.Login(ctx, providers.AuthConfig{})
	if err == nil {
		t.Fatal("Login() with expired context expected error, got nil")
	}

	// Verify the error is a ProviderError with the timeout code.
	var pe *providers.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Login() error type = %T, want *providers.ProviderError", err)
	}
	if pe.Code != 504 {
		t.Errorf("Login() error code = %d, want %d (timeout)", pe.Code, 504)
	}
}

// =============================================================================
// Tests: Prompt — error paths (no real browser)
// =============================================================================

func TestPrompt_InvalidRequest_EmptyMessages(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	_, err := p.Prompt(context.Background(), providers.ChatRequest{Model: "mimo-pro"})
	if err == nil {
		t.Fatal("Prompt() expected error for empty messages, got nil")
	}
	var pe *providers.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Prompt() error type = %T, want *providers.ProviderError", err)
	}
	if pe.Code != 400 {
		t.Errorf("Prompt() error code = %d, want 400 (invalid request)", pe.Code)
	}
}

func TestPrompt_InvalidRequest_EmptyModel(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	_, err := p.Prompt(context.Background(), providers.ChatRequest{
		Messages: []providers.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Prompt() expected error for empty model, got nil")
	}
	var pe *providers.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Prompt() error type = %T, want *providers.ProviderError", err)
	}
	if pe.Code != 400 {
		t.Errorf("Prompt() error code = %d, want 400 (invalid request)", pe.Code)
	}
}

func TestPrompt_InvalidRequest_EmptyAll(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	_, err := p.Prompt(context.Background(), providers.ChatRequest{})
	if err == nil {
		t.Fatal("Prompt() expected error for empty request, got nil")
	}
}

func TestPrompt_InvalidSession(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	// No session saved — IsSessionValid returns false, so Login is triggered
	// and fails because there's no browser environment in unit tests.
	_, err := p.Prompt(context.Background(), providers.ChatRequest{
		Model:    "mimo-pro",
		Messages: []providers.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Prompt() expected error for invalid session, got nil")
	}
	// The error is wrapped from Login's browser launch failure —
	// we just verify something went wrong.
	t.Logf("Prompt() error (expected): %v", err)
}

func TestChat_NilRequest(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	resp, err := p.Chat(nil)
	if resp != nil {
		t.Fatal("Chat(nil) expected nil response")
	}
	if err == nil {
		t.Fatal("Chat(nil) expected error")
	}
	var pe *providers.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("Chat(nil) error type = %T, want *providers.ProviderError", err)
	}
	if pe.Code != 400 {
		t.Errorf("Chat(nil) error code = %d, want 400 (invalid request)", pe.Code)
	}
}

func TestChat_Validation_EmptyMessages(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	resp, err := p.Chat(&providers.ChatRequest{Model: "mimo-pro"})
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if err == nil {
		t.Fatal("expected validation error for empty messages")
	}
}

func TestChat_Validation_EmptyModel(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	resp, err := p.Chat(&providers.ChatRequest{
		Messages: []providers.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if err == nil {
		t.Fatal("expected validation error for empty model")
	}
}

func TestChat_InvalidSession(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := New()
	// No session saved — IsSessionValid returns false, which triggers Login
	// in Prompt, and Login fails because there's no browser in unit tests.
	resp, err := p.Chat(&providers.ChatRequest{
		Model:    "mimo-pro",
		Messages: []providers.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if resp != nil {
		t.Fatal("expected nil response when no session exists")
	}
	if err == nil {
		t.Fatal("expected auth failure error when no session exists")
	}
	t.Logf("Chat() error (expected): %v", err)
}
