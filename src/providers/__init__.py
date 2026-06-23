"""Providers package — per-provider configuration, types, and abstract interface.

Re-exports the key loading functions so callers can do::

    from src.providers import load_provider_config, list_providers

instead of digging into sub-modules.

The package also defines:

* The AIProvider abstract base class that every provider implementation
  must subclass (:mod:`src.providers.base`).
* The domain types (ChatRequest, ChatResponse, etc.) used by the
  AIProvider interface (:mod:`src.providers.types`).

The high-level provider functions that CLI commands (``chat``, ``models``,
``test``) depend on via ``_safe_import``.
"""

from __future__ import annotations

from src.providers.base import AIProvider
from src.providers.config import (
    ensure_providers_dir,
    list_providers,
    load_all_providers,
    load_provider_config,
    provider_api_key,
    reset_provider_cache,
)
from src.providers.errors import (
    AuthenticationError,
    BadRequestError,
    ProviderError,
    ProviderNotAvailable,
    RateLimitError,
    SessionExpiredError,
    TimeoutError,
)
from src.providers.models import ModelConfig, ProviderConfig, RateLimitConfig
from src.providers.types import (
    ChatMessage,
    ChatRequest,
    ChatResponse,
    ChatResponseChoice,
    Usage,
)

__all__ = [
    # Config helpers
    "ensure_providers_dir",
    "list_providers",
    "load_all_providers",
    "load_provider_config",
    "provider_api_key",
    "reset_provider_cache",
    # Config models
    "ModelConfig",
    "ProviderConfig",
    "RateLimitConfig",
    # Domain types
    "ChatMessage",
    "ChatRequest",
    "ChatResponse",
    "ChatResponseChoice",
    "Usage",
    # Abstract interface
    "AIProvider",
    # Error types
    "AuthenticationError",
    "BadRequestError",
    "ProviderError",
    "ProviderNotAvailable",
    "RateLimitError",
    "SessionExpiredError",
    "TimeoutError",
]
