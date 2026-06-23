// Package providers implements adapters for each LLM provider (DeepSeek, MiMo,
// OpenAI, etc.). Each provider is registered into a global Registry with a
// unique name so the HTTP router can dispatch incoming requests to the right
// adapter.
//
// Two categories of provider exist:
//   - Direct API: forwards requests to the provider's REST API (e.g. OpenAI).
//   - Browser-based: uses headless browser automation to authenticate and
//     interact with the provider's web chat interface (e.g. DeepSeek, MiMo).
package providers

import (
	"fmt"
	"sync"
)

// =============================================================================
// Provider interface
// =============================================================================

// ModelInfo describes a single chat model offered by a provider.
type ModelInfo struct {
	ID                string `json:"id"`
	MaxTokens         int    `json:"max_tokens,omitempty"`
	SupportsStreaming bool   `json:"supports_streaming,omitempty"`
}

// Provider is the interface that every LLM provider adapter must implement.
type Provider interface {
	// Name returns a unique short name for this provider (e.g. "deepseek",
	// "mimo", "openai"). Used as the registry key.
	Name() string

	// Description returns a human-readable description.
	Description() string

	// Models returns the list of chat models this provider offers.
	Models() []ModelInfo

	// Chat sends a chat completion request and returns the response.
	// Implementations may be direct API calls or browser-based automation.
	Chat(req *ChatRequest) (*ChatResponse, error)
}

// =============================================================================
// Registry
// =============================================================================

// Registry is a thread-safe map of named providers. It serves as the shared
// registry that cmd/main.go populates at startup and the HTTP router queries
// at request time.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry. Returns an error if a provider
// with the same name is already registered.
func (r *Registry) Register(p Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q: already registered", name)
	}
	r.providers[name] = p
	return nil
}

// Get returns the provider with the given name, or nil if not found.
func (r *Registry) Get(name string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

// All returns a snapshot of all registered providers, keyed by name.
func (r *Registry) All() map[string]Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Provider, len(r.providers))
	for k, v := range r.providers {
		out[k] = v
	}
	return out
}

// Len returns the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}
