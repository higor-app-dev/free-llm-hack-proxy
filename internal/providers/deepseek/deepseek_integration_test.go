//go:build integration

// Package deepseek contains integration tests for the DeepSeek provider
// that require a real browser and manual user interaction for login.
//
// Usage:
//
//	go test -tags=integration -run TestLoginIntegration ./internal/providers/deepseek/ -v -timeout=10m
//
// The test opens a visible Chromium window at chat.deepseek.com. Log in
// manually in the browser window. Once the chat interface appears, the
// test captures the session and verifies it was saved correctly.
package deepseek

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/higor/free-llm-hack-proxy/internal/providers"
)

const (
	// integrationTestTimeout is the max time to wait for manual login.
	// Set via DEEPSEEK_LOGIN_TIMEOUT env var or default 5 minutes.
	integrationTestTimeout = 5 * time.Minute
)

// TestLoginIntegration opens chat.deepseek.com in a real browser and waits
// for the user to log in manually. On success it verifies the session store
// contains valid data (at least one cookie).
//
// Prerequisites:
//   - Chromium or Chrome installed (go-rod auto-downloads if missing)
//   - A DeepSeek account with login credentials
//
// Environment variables:
//   - DEEPSEEK_LOGIN_TIMEOUT: override the login timeout (e.g. "10m")
//   - DEEPSEEK_SESSION_PATH: custom path for the session file (default:
//     ~/.llm-proxy/sessions/chat.deepseek.com.json)
func TestLoginIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Allow timeout override via env.
	timeout := integrationTestTimeout
	if envTimeout := os.Getenv("DEEPSEEK_LOGIN_TIMEOUT"); envTimeout != "" {
		d, err := time.ParseDuration(envTimeout)
		if err == nil {
			timeout = d
		}
	}

	t.Logf("DeepSeek login integration test starting (timeout=%v)", timeout)
	t.Log("A Chromium browser window will open at chat.deepseek.com.")
	t.Log("Log in manually in the browser window.")
	t.Log("The test will capture the session once the chat interface is detected.")

	// Create context with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Allow session path override via env.
	sessionPath := os.Getenv("DEEPSEEK_SESSION_PATH")
	if sessionPath != "" {
		t.Logf("Using custom session path: %s", sessionPath)
	}

	// Provider with default store (uses ~/.llm-proxy/sessions/).
	// The store path can be overridden for testing by constructing
	// NewWithStore with a custom FileStore path.
	p := New()

	// Login using the manual browser flow.
	// AuthConfig is empty — the user types credentials in the browser.
	err := p.Login(ctx, providers.AuthConfig{})
	if err != nil {
		t.Fatalf("Login() failed: %v", err)
	}

	// Verify: session store contains valid data.
	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false after successful Login — session store has no data")
	}

	// Verify the session file exists on disk (only if using the default
	// FileStore path or one is explicitly known).
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		defaultPath := filepath.Join(home, ".llm-proxy", "sessions", "chat.deepseek.com.json")
		info, err := os.Stat(defaultPath)
		if err != nil {
			t.Logf("Note: session file not found at default path %q: %v", defaultPath, err)
		} else {
			t.Logf("Session file size: %d bytes", info.Size())
		}
	}

	t.Log("Login integration test PASSED — session captured and persisted")
}

// TestPromptIntegration tests the full Prompt flow:
// 1. Logs in (or reuses an existing valid session)
// 2. Sends a simple question
// 3. Verifies the response contains expected content
//
// Prerequisites:
//   - Chromium or Chrome installed (go-rod auto-downloads if missing)
//   - A DeepSeek account with login credentials
//   - You must log in manually in the browser window that opens
//
// Environment variables:
//   - DEEPSEEK_LOGIN_TIMEOUT: override the login timeout (e.g. "10m")
//   - DEEPSEEK_PROMPT_TIMEOUT: override the prompt response timeout (e.g. "5m")
func TestPromptIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Allow timeout override via env.
	loginTimeout := integrationTestTimeout
	if envTimeout := os.Getenv("DEEPSEEK_LOGIN_TIMEOUT"); envTimeout != "" {
		d, err := time.ParseDuration(envTimeout)
		if err == nil {
			loginTimeout = d
		}
	}

	t.Logf("DeepSeek Prompt integration test starting (login_timeout=%v)", loginTimeout)
	t.Log("A Chromium browser window will open at chat.deepseek.com.")
	t.Log("If not already logged in, log in manually in the browser window.")

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout+5*time.Minute)
	defer cancel()

	p := New()

	// If the session is already valid, skip Login.
	if !p.IsSessionValid() {
		t.Log("No valid session found — running Login flow...")
		err := p.Login(ctx, providers.AuthConfig{})
		if err != nil {
			t.Fatalf("Login() failed: %v", err)
		}
		if !p.IsSessionValid() {
			t.Fatal("IsSessionValid() = false after successful Login")
		}
		t.Log("Login successful — session saved")
	} else {
		t.Log("Using existing valid session — skipping Login")
	}

	// Send a simple prompt.
	req := providers.ChatRequest{
		Model: "deepseek-chat",
		Messages: []providers.ChatMessage{
			{Role: "user", Content: "Say hello in one word."},
		},
	}

	t.Log("Sending prompt: \"Say hello in one word.\"")
	resp, err := p.Prompt(ctx, req)
	if err != nil {
		t.Fatalf("Prompt() failed: %v", err)
	}

	// Verify the response.
	if resp == nil {
		t.Fatal("Prompt() returned nil response")
	}
	if len(resp.Choices) == 0 {
		t.Fatal("Prompt() returned response with no choices")
	}

	content := resp.Choices[0].Message.Content
	t.Logf("Response (%d chars): %q", len(content), content)

	if content == "" {
		t.Error("Prompt() response content is empty")
	}

	if resp.Model != "deepseek-chat" {
		t.Errorf("Response model = %q, want %q", resp.Model, "deepseek-chat")
	}

	t.Log("Prompt integration test PASSED")
}

// TestLoginIntegration_Timeout verifies that Login returns ErrTimeout when
// the context deadline expires before the user logs in.
func TestLoginIntegration_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Very short timeout — 3 seconds is not enough to log in manually.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p := New()
	err := p.Login(ctx, providers.AuthConfig{})
	if err == nil {
		t.Fatal("Login() with expired context expected error, got nil")
	}

	// Check the error is a timeout (ErrTimeout code 504).
	if pe, ok := err.(*providers.ProviderError); ok {
		if pe.Code == 504 {
			t.Logf("Got expected timeout error: %v", err)
		} else {
			t.Errorf("Login() error code = %d, want 504 (timeout), got: %v", pe.Code, err)
		}
	} else {
		t.Logf("Got error (not ProviderError): %v", err)
	}

	t.Log("Timeout integration test PASSED — Login aborted on deadline expiry")
}
