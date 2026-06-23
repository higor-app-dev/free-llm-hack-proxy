"""Custom exception types for LLM provider interactions.

Defines a typed exception hierarchy that all providers use to signal
specific failure modes.  The proxy layer catches these and maps them
to appropriate HTTP responses.

Usage::

    from src.providers.errors import (
        AuthenticationError, RateLimitError, ProviderError,
    )

    raise AuthenticationError("Invalid API key")
    raise RateLimitError("Rate limit exceeded", retry_after=30)

Exception hierarchy::

    ProviderError              (base — all provider errors)
    ├── BadRequestError        (400 — malformed request)
    ├── AuthenticationError    (401 — invalid/missing API key)
    ├── RateLimitError         (429 — too many requests)
    ├── TimeoutError           (network timeout)
    ├── SessionExpiredError    (browser session expired)
    └── ProviderNotAvailable   (provider is down or unreachable)
"""

from __future__ import annotations

from typing import Optional


class ProviderError(Exception):
    """Base exception for all provider-related errors.

    All custom provider exceptions inherit from this so callers can
    catch ``ProviderError`` to handle any provider failure uniformly.

    Attributes
    ----------
    message :
        Human-readable description of what went wrong.
    status_code :
        Suggested HTTP status code (may be ``None`` for internal errors).
    """

    def __init__(
        self,
        message: str = "A provider error occurred",
        *,
        status_code: Optional[int] = None,
    ) -> None:
        self.status_code = status_code
        super().__init__(message)


class BadRequestError(ProviderError):
    """The request was malformed or contained invalid parameters.

    Corresponds to HTTP 400 Bad Request.
    """

    def __init__(self, message: str = "Bad request") -> None:
        super().__init__(message, status_code=400)


class AuthenticationError(ProviderError):
    """The API key or credentials are missing or invalid.

    Corresponds to HTTP 401 Unauthorized.
    """

    def __init__(self, message: str = "Authentication failed") -> None:
        super().__init__(message, status_code=401)


class RateLimitError(ProviderError):
    """The provider returned a rate-limit response (429 Too Many Requests).

    Attributes
    ----------
    retry_after :
        Number of seconds to wait before retrying, if known.
    """

    def __init__(
        self,
        message: str = "Rate limit exceeded",
        *,
        retry_after: Optional[float] = None,
    ) -> None:
        self.retry_after = retry_after
        super().__init__(message, status_code=429)


class TimeoutError(ProviderError):
    """The provider did not respond within the configured timeout.

    Corresponds to HTTP 504 Gateway Timeout when bubbled to the proxy.
    """

    def __init__(self, message: str = "Provider request timed out") -> None:
        super().__init__(message, status_code=504)


class SessionExpiredError(ProviderError):
    """The provider's browser session has expired and needs re-login.

    Used for cookie/auth-based providers (e.g. ChatGPT, Groq Free).
    Corresponds to HTTP 401 Unauthorized.
    """

    def __init__(self, message: str = "Provider session expired") -> None:
        super().__init__(message, status_code=401)


class ProviderNotAvailable(ProviderError):
    """The provider is unreachable, down, or returned a server error.

    Corresponds to HTTP 502 Bad Gateway or 503 Service Unavailable.
    """

    def __init__(
        self,
        message: str = "Provider is not available",
        *,
        status_code: int = 502,
    ) -> None:
        super().__init__(message, status_code=status_code)


__all__ = [
    "AuthenticationError",
    "BadRequestError",
    "ProviderError",
    "ProviderNotAvailable",
    "RateLimitError",
    "SessionExpiredError",
    "TimeoutError",
]
