// Package providers implements LLM provider adapters with a shared type
// system for OpenAI-compatible chat requests and responses.
package providers

import "fmt"

// =============================================================================
// ProviderError — structured provider error with error-chain support
// =============================================================================

// ProviderError represents a structured error from an LLM provider with an
// error code, human-readable message, and optional detail context. It
// participates in the standard error chain via Unwrap() and supports both
// errors.Is matching (by Code + Message) and type assertion.
type ProviderError struct {
	Code    int
	Message string
	Details map[string]interface{}
}

// Error returns a human-readable representation of the error.
func (e *ProviderError) Error() string {
	if len(e.Details) > 0 {
		return fmt.Sprintf("[%d] %s (details: %v)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap returns nil, making ProviderError the terminal element in an
// error chain. It satisfies the error-wrapping contract so that callers
// can freely chain ProviderError with fmt.Errorf("... %w ...", err).
func (e *ProviderError) Unwrap() error { return nil }

// Is enables errors.Is matching by Code and Message rather than by pointer
// identity, so both sentinel variables and dynamically constructed
// ProviderError values with the same Code+Message are treated as equal.
func (e *ProviderError) Is(target error) bool {
	t, ok := target.(*ProviderError)
	if !ok {
		return false
	}
	return e.Code == t.Code && e.Message == t.Message
}

// =============================================================================
// Sentinel errors
// =============================================================================

var (
	// ErrInvalidRequest is returned when the request payload fails
	// provider-side validation (HTTP 4xx-class semantic errors).
	ErrInvalidRequest = &ProviderError{Code: 400, Message: "invalid request"}

	// ErrAuthFailure indicates missing, expired, or rejected credentials.
	ErrAuthFailure = &ProviderError{Code: 401, Message: "authentication failure"}

	// ErrModelUnavailable means the requested model is not currently
	// served, deprecated, or overloaded (HTTP 503).
	ErrModelUnavailable = &ProviderError{Code: 503, Message: "model unavailable"}

	// ErrRateLimited signals the provider's rate-limit or quota has been
	// exceeded (HTTP 429). Callers should back off and retry.
	ErrRateLimited = &ProviderError{Code: 429, Message: "rate limited"}

	// ErrTimeout indicates the upstream provider did not respond within
	// the configured deadline (HTTP 504 equivalent).
	ErrTimeout = &ProviderError{Code: 504, Message: "timeout"}
)
