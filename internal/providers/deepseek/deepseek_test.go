// Package deepseek implements the DeepSeek AIProvider adapter.
//
// This file contains unit tests for the DeepSeek provider. Integration tests
// that require a real browser are in deepseek_integration_test.go.
package deepseek

import (
	"sync"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/higor/free-llm-hack-proxy/internal/providers"
)

// =============================================================================
// Mock SessionStore
// =============================================================================

// mockStore implements the session.SessionStore interface for testing without
// filesystem or browser dependencies.
type mockStore struct {
	mu           sync.Mutex
	savedCookies []*proto.NetworkCookie
	savedLS      map[string]string
	loadCookies  []*proto.NetworkCookie
	loadLS       map[string]string
	loadErr      error
	saveErr      error
	loadCount    int
	saveCount    int
}

func newMockStore() *mockStore {
	return &mockStore{
		loadCookies: nil,
		loadLS:      nil,
	}
}

// loadValid returns a mock store pre-populated with a valid session.
func loadValid() *mockStore {
	return &mockStore{
		loadCookies: []*proto.NetworkCookie{
			{Name: "cloud_session", Value: "abc123", Domain: ".deepseek.com", Path: "/"},
		},
		loadLS: map[string]string{"token": "eyJhbG"},
	}
}

func (m *mockStore) Save(cookies []*proto.NetworkCookie, localStorage map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCount++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.savedCookies = cookies
	m.savedLS = localStorage
	return nil
}

func (m *mockStore) Load() ([]*proto.NetworkCookie, map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadCount++
	return m.loadCookies, m.loadLS, m.loadErr
}

// =============================================================================
// Tests: Constructor
// =============================================================================

func TestNew_InitializesStore(t *testing.T) {
	p := New()
	if p.store == nil {
		t.Fatal("New() expected non-nil store")
	}
}

func TestNewWithStore_UsesCustomStore(t *testing.T) {
	ms := newMockStore()
	p := NewWithStore(ms)
	if p.store != ms {
		t.Fatal("NewWithStore() did not use the provided store")
	}
}

// =============================================================================
// Tests: Name
// =============================================================================

func TestName(t *testing.T) {
	p := New()
	if got := p.Name(); got != "deepseek" {
		t.Errorf("Name() = %q, want %q", got, "deepseek")
	}
}

// =============================================================================
// Tests: Description
// =============================================================================

func TestDescription(t *testing.T) {
	p := New()
	if got := p.Description(); got == "" {
		t.Error("Description() should not be empty")
	}
}

// =============================================================================
// Tests: Models
// =============================================================================

func TestModels(t *testing.T) {
	p := New()
	models := p.Models()
	if len(models) == 0 {
		t.Fatal("Models() returned empty slice")
	}

	found := make(map[string]bool)
	for _, m := range models {
		if m.ID == "" {
			t.Error("Models() returned an entry with empty ID")
		}
		if found[m.ID] {
			t.Errorf("Models() has duplicate ID: %q", m.ID)
		}
		found[m.ID] = true
	}

	if !found["deepseek-chat"] {
		t.Errorf("Models() missing 'deepseek-chat'")
	}
	if !found["deepseek-reasoner"] {
		t.Errorf("Models() missing 'deepseek-reasoner'")
	}
}

// =============================================================================
// Tests: IsSessionValid
// =============================================================================

func TestIsSessionValid_NoSession(t *testing.T) {
	ms := newMockStore()
	p := NewWithStore(ms)

	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (no session saved)")
	}
	if ms.loadCount != 1 {
		t.Errorf("expected 1 Load call, got %d", ms.loadCount)
	}
}

func TestIsSessionValid_EmptyCookies(t *testing.T) {
	ms := newMockStore()
	ms.loadCookies = []*proto.NetworkCookie{} // empty slice, not nil
	p := NewWithStore(ms)

	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (empty cookies)")
	}
}

func TestIsSessionValid_ValidSession(t *testing.T) {
	ms := loadValid()
	p := NewWithStore(ms)

	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false, want true (valid session)")
	}
}

func TestIsSessionValid_LoadError(t *testing.T) {
	ms := newMockStore()
	ms.loadErr = assertError("load failed")
	p := NewWithStore(ms)

	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (load error)")
	}
}

func TestIsSessionValid_ExpiredSession(t *testing.T) {
	ms := newMockStore()
	ms.loadCookies = []*proto.NetworkCookie{
		{Name: "cloud_session", Value: "abc123", Domain: ".deepseek.com", Path: "/", Expires: 1_000_000}, // unix timestamp in year 2001
	}
	p := NewWithStore(ms)

	if p.IsSessionValid() {
		t.Error("IsSessionValid() = true, want false (all cookies expired)")
	}
}

func TestIsSessionValid_MixedExpiredAndValid(t *testing.T) {
	ms := newMockStore()
	ms.loadCookies = []*proto.NetworkCookie{
		{Name: "old_session", Value: "expired", Expires: 1_000_000},                           // expired
		{Name: "valid_session", Value: "still-good", Domain: ".deepseek.com", Path: "/"}, // Expires=0 = session cookie
	}
	p := NewWithStore(ms)

	if !p.IsSessionValid() {
		t.Error("IsSessionValid() = false, want true (valid cookie present alongside expired)")
	}
}

// =============================================================================
// Tests: Close
// =============================================================================

func TestClose_ReturnsNil(t *testing.T) {
	p := New()
	if err := p.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}

// =============================================================================
// Tests: Chat (delegates to Prompt)
// =============================================================================

func TestChat_NilRequest(t *testing.T) {
	p := New()
	resp, err := p.Chat(nil)
	if resp != nil {
		t.Fatal("Chat(nil) expected nil response")
	}
	if err == nil {
		t.Fatal("Chat(nil) expected error")
	}
	if pe, ok := err.(*providers.ProviderError); ok {
		if pe.Code != 400 {
			t.Errorf("Chat(nil) error code = %d, want 400 (invalid request)", pe.Code)
		}
	} else {
		t.Errorf("Chat(nil) error type = %T, want *providers.ProviderError", err)
	}
}

func TestChat_Validation_EmptyMessages(t *testing.T) {
	p := New()
	resp, err := p.Chat(&providers.ChatRequest{Model: "deepseek-chat"})
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if err == nil {
		t.Fatal("expected validation error for empty messages")
	}
}

func TestChat_Validation_EmptyModel(t *testing.T) {
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

// =============================================================================
// Tests: Prompt
// =============================================================================

func TestPrompt_InvalidRequest_EmptyMessages(t *testing.T) {
	p := NewWithStore(newMockStore())
	_, err := p.Prompt(nil, providers.ChatRequest{Model: "deepseek-chat"})
	if err == nil {
		t.Fatal("Prompt() expected error for empty messages, got nil")
	}
	if pe, ok := err.(*providers.ProviderError); ok {
		if pe.Code != 400 {
			t.Errorf("Prompt() error code = %d, want 400 (invalid request)", pe.Code)
		}
	} else {
		t.Errorf("Prompt() error type = %T, want *providers.ProviderError", err)
	}
}

func TestPrompt_InvalidRequest_EmptyModel(t *testing.T) {
	p := NewWithStore(newMockStore())
	_, err := p.Prompt(nil, providers.ChatRequest{
		Messages: []providers.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Prompt() expected error for empty model, got nil")
	}
	if pe, ok := err.(*providers.ProviderError); ok {
		if pe.Code != 400 {
			t.Errorf("Prompt() error code = %d, want 400 (invalid request)", pe.Code)
		}
	} else {
		t.Errorf("Prompt() error type = %T, want *providers.ProviderError", err)
	}
}

func TestPrompt_InvalidRequest_Nil(t *testing.T) {
	p := NewWithStore(newMockStore())
	_, err := p.Prompt(nil, providers.ChatRequest{})
	if err == nil {
		t.Fatal("Prompt() expected error for nil/empty request, got nil")
	}
}

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
	// system prefix is NOT added — only user/assistant get role prefixes.
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

	params = cookiesToParams([]*proto.NetworkCookie{})
	if len(params) != 0 {
		t.Errorf("cookiesToParams(empty) = %d, want 0", len(params))
	}
}

func TestCookiesToParams_ConvertsAllFields(t *testing.T) {
	cookies := []*proto.NetworkCookie{
		{
			Name:     "session",
			Value:    "abc123",
			Domain:   ".deepseek.com",
			Path:     "/",
			Secure:   true,
			HTTPOnly: true,
			SameSite: proto.NetworkCookieSameSite("Lax"),
			Expires:  proto.TimeSinceEpoch(9999999999),
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
	if p.Domain != ".deepseek.com" {
		t.Errorf("Domain = %q, want %q", p.Domain, ".deepseek.com")
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

// assertError is a simple error implementation for testing.
type assertError string

func (e assertError) Error() string { return string(e) }
