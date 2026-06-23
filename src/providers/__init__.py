"""Providers package — per-provider configuration and factory.

Re-exports the key loading functions so callers can do::

    from src.providers import load_provider_config, list_providers

instead of digging into sub-modules.

The package also defines the high-level provider functions that CLI commands
(``chat``, ``models``, ``test``) depend on via ``_safe_import``.
"""

from __future__ import annotations

from src.providers.config import (
    ensure_providers_dir,
    list_providers,
    load_all_providers,
    load_provider_config,
    provider_api_key,
    reset_provider_cache,
)
from src.providers.models import ModelConfig, ProviderConfig, RateLimitConfig

__all__ = [
    "ensure_providers_dir",
    "list_providers",
    "load_all_providers",
    "load_provider_config",
    "provider_api_key",
    "reset_provider_cache",
    "ModelConfig",
    "ProviderConfig",
    "RateLimitConfig",
]
