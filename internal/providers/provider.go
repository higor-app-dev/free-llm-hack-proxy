package providers

import "context"

// =============================================================================
// AuthConfig — credentials for browser-based provider authentication
// =============================================================================

// AuthConfig carries the credentials and configuration needed to authenticate
// a browser-based provider (DeepSeek, MiMo, etc.) via Login.
type AuthConfig struct {
	// Email or username for the provider account.
	Username string `json:"username,omitempty"`
	// Password for the provider account.
	Password string `json:"password,omitempty"`
	// AuthToken is an optional pre-obtained authentication token (cookie, JWT,
	// bearer token) that can be used instead of username/password login.
	AuthToken string `json:"auth_token,omitempty"`
	// BaseURL is the provider's API or web-chat base URL, if different from
	// the default.
	BaseURL string `json:"base_url,omitempty"`
	// Extra is a bag of provider-specific key-value pairs that don't fit into
	// the standard fields (e.g. API version, tenant ID, custom headers).
	Extra map[string]string `json:"extra,omitempty"`
}

// =============================================================================
// AIProvider — interface for browser-automated providers
// =============================================================================

// AIProvider is the interface that browser-automated LLM providers must
// implement. Unlike the simpler Provider interface (which covers direct-API
// adapters), AIProvider handles providers that need browser-based
// authentication, session lifecycle management, and model discovery.
//
// Implementations include DeepSeek, MiMo, and any other web-chat-based
// provider that requires headless browser login and periodic session
// re-validation.
type AIProvider interface {
	// Name returns a unique short name for this provider (e.g. "deepseek",
	// "mimo"). Used as the registry key.
	Name() string

	// Models returns the list of model IDs this provider exposes. An error is
	// returned if the provider cannot be reached or the session is invalid.
	Models() ([]string, error)

	// Login authenticates the provider using the given AuthConfig. For
	// browser-based providers this typically launches a headless browser,
	// navigates to the provider's login page, fills in credentials, and
	// persists the resulting session.
	//
	// Returns a ProviderError with ErrAuthFailure if credentials are rejected,
	// or ErrTimeout if the login page does not respond.
	Login(ctx context.Context, config AuthConfig) error

	// IsSessionValid checks whether the current authenticated session is still
	// valid (cookies/tokens have not expired, the provider has not logged the
	// session out server-side). Returns true if the session can be used for
	// prompts.
	IsSessionValid() bool

	// Prompt sends a chat completion request using the authenticated session
	// and returns the response. The caller should validate the ChatRequest
	// (via its Validate method) before calling Prompt.
	//
	// Returns a ProviderError with ErrInvalidRequest for malformed input,
	// ErrRateLimited when the provider enforces a rate limit, or ErrTimeout
	// on upstream timeout.
	Prompt(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// Close releases any resources held by the provider (browser instance,
	// open connections, temporary files). After Close, the provider should
	// not be used again without a fresh Login.
	Close() error
}
