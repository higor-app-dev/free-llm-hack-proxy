package providers

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock provider
// =============================================================================

type mockProvider struct {
	name string
	desc string
	mdls []ModelInfo
}

func (m *mockProvider) Name() string                { return m.name }
func (m *mockProvider) Description() string         { return m.desc }
func (m *mockProvider) Models() []ModelInfo          { return m.mdls }
func (m *mockProvider) Chat(_ *ChatRequest) (*ChatResponse, error) {
	return nil, nil
}

func newMock(name, desc string) *mockProvider {
	return &mockProvider{name: name, desc: desc}
}

// =============================================================================
// Tests
// =============================================================================

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	require.NotNil(t, r, "NewRegistry should return a non-nil registry")
	assert.Equal(t, 0, r.Len(), "fresh registry should be empty")
}

func TestRegister_Single(t *testing.T) {
	r := NewRegistry()
	p := newMock("openai", "OpenAI API")

	err := r.Register(p)
	require.NoError(t, err, "first registration should succeed")
	assert.Equal(t, 1, r.Len(), "registry should have 1 provider")

	got := r.Get("openai")
	require.NotNil(t, got, "Get should return the registered provider")
	assert.Equal(t, "openai", got.Name())
	assert.Equal(t, "OpenAI API", got.Description())
}

func TestRegister_Multiple(t *testing.T) {
	r := NewRegistry()

	p1 := newMock("openai", "OpenAI API")
	p2 := newMock("deepseek", "DeepSeek API")
	p3 := newMock("mimo", "MiMo API")

	require.NoError(t, r.Register(p1))
	require.NoError(t, r.Register(p2))
	require.NoError(t, r.Register(p3))

	assert.Equal(t, 3, r.Len(), "registry should have 3 providers")

	assert.NotNil(t, r.Get("openai"))
	assert.NotNil(t, r.Get("deepseek"))
	assert.NotNil(t, r.Get("mimo"))
}

func TestRegister_DuplicateError(t *testing.T) {
	r := NewRegistry()
	p := newMock("openai", "OpenAI API")

	err := r.Register(p)
	require.NoError(t, err)

	// Register the same provider again — should fail.
	dup := newMock("openai", "OpenAI Again")
	err = r.Register(dup)
	require.Error(t, err, "duplicate registration should error")
	assert.Contains(t, err.Error(), `provider "openai"`)
	assert.Contains(t, err.Error(), "already registered")

	// Registry should still contain the original.
	assert.Equal(t, 1, r.Len())
	assert.Equal(t, "OpenAI API", r.Get("openai").Description())
}

func TestGet_UnknownReturnsNil(t *testing.T) {
	r := NewRegistry()

	got := r.Get("nonexistent")
	assert.Nil(t, got, "Get for unknown name should return nil")

	// Also verify after registering some providers.
	require.NoError(t, r.Register(newMock("openai", "OpenAI")))
	assert.Nil(t, r.Get("unknown"), "Get for unknown name should still return nil")
	assert.NotNil(t, r.Get("openai"), "Get for registered name should return provider")
}

func TestAll_Snapshot(t *testing.T) {
	r := NewRegistry()

	p1 := newMock("alpha", "Provider Alpha")
	p2 := newMock("beta", "Provider Beta")
	require.NoError(t, r.Register(p1))
	require.NoError(t, r.Register(p2))

	all := r.All()
	assert.Len(t, all, 2, "All() should return all registered providers")
	assert.Equal(t, p1, all["alpha"])
	assert.Equal(t, p2, all["beta"])
}

func TestAll_MapIsolation(t *testing.T) {
	// All() must return a copy — modifying the returned map must not affect
	// the registry internals.
	r := NewRegistry()
	require.NoError(t, r.Register(newMock("openai", "OpenAI API")))

	// Get the snapshot and modify it.
	snapshot := r.All()
	snapshot["injected"] = newMock("injected", "Should not appear")
	delete(snapshot, "openai")

	// The registry must be unchanged.
	assert.Equal(t, 1, r.Len(), "registry len should be unchanged after modifying snapshot")
	assert.NotNil(t, r.Get("openai"), "original entry should survive snapshot deletion")
	assert.Nil(t, r.Get("injected"), "injected entry should not appear in registry")
}

func TestLen(t *testing.T) {
	r := NewRegistry()
	assert.Equal(t, 0, r.Len(), "empty registry")

	require.NoError(t, r.Register(newMock("a", "")))
	assert.Equal(t, 1, r.Len())

	require.NoError(t, r.Register(newMock("b", "")))
	assert.Equal(t, 2, r.Len())
}

func TestAll_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	all := r.All()
	assert.NotNil(t, all, "All() on empty registry should return non-nil map")
	assert.Len(t, all, 0)
}

// =============================================================================
// Concurrent access safety (the -race test)
// =============================================================================

func TestConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		i := i
		go func() {
			defer wg.Done()
			name := "provider_" + string(rune('a'+i%26))
			// Interleave Register and Get calls.
			_ = r.Register(newMock(name, ""))
			_ = r.Get(name)
			_ = r.Get("nonexistent")
			_ = r.All()
			_ = r.Len()
		}()
	}
	wg.Wait()

	// Final sanity check: all registered providers should be findable.
	assert.GreaterOrEqual(t, r.Len(), 1, "at least one provider survived concurrent registration")
}

func TestConcurrentRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	providers := []string{"a", "b", "c", "d", "e", "f", "g", "h"}

	var wg sync.WaitGroup

	// Register all providers concurrently.
	for _, name := range providers {
		wg.Add(1)
		name := name
		go func() {
			defer wg.Done()
			_ = r.Register(newMock(name, "provider "+name))
		}()
	}

	// Simultaneously query the registry.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, name := range providers {
				r.Get(name)
				r.All()
				r.Len()
			}
		}()
	}

	wg.Wait()

	// Every successfully-registered provider must be retrievable.
	for _, name := range providers {
		p := r.Get(name)
		if p != nil {
			assert.Equal(t, "provider "+name, p.Description())
		}
	}
}

func TestConcurrentAllMapIsolation(t *testing.T) {
	// Ensure that All() returns a unique snapshot even under heavy concurrent
	// Register and All() calls.
	r := NewRegistry()
	require.NoError(t, r.Register(newMock("base", "base provider")))

	var wg sync.WaitGroup

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap := r.All()
			// Modify the snapshot locally — this must never panic or corrupt
			// the registry internals.
			snap["temp"] = newMock("temp", "temp")
			delete(snap, "base")
		}()
	}

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Register(newMock("extra", "extra"))
		}()
	}

	wg.Wait()

	// The "base" provider must still be present in the registry.
	assert.NotNil(t, r.Get("base"), "base provider must survive concurrent All modifications")
}
