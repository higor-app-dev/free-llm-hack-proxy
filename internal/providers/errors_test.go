package providers

import (
	"errors"
	"fmt"
	"testing"
)

func TestProviderError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *ProviderError
		want string
	}{
		{"no details", &ProviderError{Code: 400, Message: "invalid request"}, "[400] invalid request"},
		{"with details", &ProviderError{Code: 429, Message: "rate limited", Details: map[string]interface{}{"retry_after": 30}}, "[429] rate limited (details: map[retry_after:30])"},
		{"empty", &ProviderError{Code: 0, Message: ""}, "[0] "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSentinelErrors_errorsIs(t *testing.T) {
	// Each sentinel must match itself via errors.Is.
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrInvalidRequest", ErrInvalidRequest},
		{"ErrAuthFailure", ErrAuthFailure},
		{"ErrModelUnavailable", ErrModelUnavailable},
		{"ErrRateLimited", ErrRateLimited},
		{"ErrTimeout", ErrTimeout},
	}
	for _, s := range sentinels {
		t.Run(s.name+"_self", func(t *testing.T) {
			if !errors.Is(s.err, s.err) {
				t.Errorf("errors.Is(%v, %v) = false, want true", s.err, s.err)
			}
		})
	}

	// Dynamically-created errors with same Code+Message must also match.
	t.Run("dynamic_match", func(t *testing.T) {
		if !errors.Is(&ProviderError{Code: 400, Message: "invalid request"}, ErrInvalidRequest) {
			t.Error("dynamic ErrInvalidRequest should match via errors.Is")
		}
		if !errors.Is(&ProviderError{Code: 504, Message: "timeout"}, ErrTimeout) {
			t.Error("dynamic ErrTimeout should match via errors.Is")
		}
	})

	// Different Code should not match.
	t.Run("code_mismatch", func(t *testing.T) {
		if errors.Is(&ProviderError{Code: 500, Message: "invalid request"}, ErrInvalidRequest) {
			t.Error("Code 500 should NOT match ErrInvalidRequest")
		}
	})

	// Different Message should not match.
	t.Run("message_mismatch", func(t *testing.T) {
		if errors.Is(&ProviderError{Code: 400, Message: "bad request"}, ErrInvalidRequest) {
			t.Error(`"bad request" should NOT match ErrInvalidRequest`)
		}
	})
}

func TestProviderError_errorsAs(t *testing.T) {
	t.Run("sentinel_type_assertion", func(t *testing.T) {
		var pe *ProviderError
		if !errors.As(ErrAuthFailure, &pe) {
			t.Fatal("errors.As(ErrAuthFailure) failed")
		}
		if pe.Code != 401 || pe.Message != "authentication failure" {
			t.Errorf("got Code=%d Message=%q, want Code=401 Message=\"authentication failure\"", pe.Code, pe.Message)
		}
	})

	t.Run("dynamic_type_assertion", func(t *testing.T) {
		dynamic := &ProviderError{Code: 503, Message: "model unavailable", Details: map[string]interface{}{"model": "gpt-4"}}
		var pe *ProviderError
		if !errors.As(dynamic, &pe) {
			t.Fatal("errors.As(dynamic error) failed")
		}
		if pe.Code != 503 || pe.Message != "model unavailable" {
			t.Errorf("got Code=%d Message=%q, want Code=503 Message=\"model unavailable\"", pe.Code, pe.Message)
		}
	})
}

func TestUnwrap(t *testing.T) {
	// Unwrap should return nil (terminal in the chain).
	if unwrapped := ErrInvalidRequest.Unwrap(); unwrapped != nil {
		t.Errorf("Unwrap() = %v, want nil", unwrapped)
	}
}

func TestWrapped_errorsIs(t *testing.T) {
	// fmt.Errorf with %w should still allow errors.Is to match the sentinel.
	wrapped := fmt.Errorf("context: %w", ErrRateLimited)
	if !errors.Is(wrapped, ErrRateLimited) {
		t.Error("fmt.Errorf(... %w ErrRateLimited) should still match ErrRateLimited via errors.Is")
	}
}
