#!/usr/bin/env python3
"""Global configuration management for the free-llm-hack-proxy.

Reads config from ``~/.llm-proxy/config.yaml`` with sensible defaults for
every key.  Environment variables prefixed ``LLM_PROXY_`` override YAML
values (e.g. ``LLM_PROXY_DEFAULT_PROVIDER`` wins over the YAML key
``default_provider``).

Priority (highest wins):
  1. Environment variables (``LLM_PROXY_*``)
  2. YAML file (``~/.llm-proxy/config.yaml``)
  3. Hard-coded Python defaults

Usage::

    from src.config import load_config

    cfg = load_config()
    print(cfg.default_provider)
"""

from __future__ import annotations

import os
import sys
from pathlib import Path
from typing import Literal, Optional

import yaml
from pydantic import BaseModel, Field, ValidationError

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

CONFIG_DIR = Path.home() / ".llm-proxy"
CONFIG_FILE = CONFIG_DIR / "config.yaml"
ENV_PREFIX = "LLM_PROXY_"

LogLevel = Literal["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"]
CacheBackend = Literal["sqlite", "redis", "memory"]


# ---------------------------------------------------------------------------
# Server sub-config
# ---------------------------------------------------------------------------

class ServerConfig(BaseModel):
    """Settings for the proxy HTTP server."""

    host: str = Field(default="0.0.0.0", description="Bind address")
    port: int = Field(default=8080, ge=1, le=65535, description="Listen port")
    workers: int = Field(default=1, ge=1, le=32, description="Worker processes")
    reload: bool = Field(default=False, description="Auto-reload on code changes")

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# Logging sub-config
# ---------------------------------------------------------------------------

class LoggingConfig(BaseModel):
    """Settings controlling log output."""

    level: LogLevel = Field(default="INFO", description="Logging verbosity")
    format: str = Field(
        default="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        description="Log format string",
    )

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# Cache sub-config
# ---------------------------------------------------------------------------

class CacheConfig(BaseModel):
    """Settings for response caching."""

    backend: CacheBackend = Field(default="sqlite", description="Cache storage backend")
    path: str = Field(
        default=str(Path.home() / ".llm-proxy" / "cache.db"),
        description="Path to cache database (sqlite only)",
    )
    ttl_seconds: int = Field(default=300, ge=0, description="Cache entry TTL in seconds")

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# Top-level Config
# ---------------------------------------------------------------------------

class Config(BaseModel):
    """Global proxy configuration.

    Fields are populated in priority order:
      1. Hard-coded defaults from ``Field(default=...)``
      2. Values from ``~/.llm-proxy/config.yaml``
      3. Environment variables with prefix ``LLM_PROXY_``
    """

    default_provider: str = Field(
        default="auto",
        description="Default provider name (or 'auto' to auto-detect)",
    )
    providers_dir: str = Field(
        default=str(CONFIG_DIR / "providers"),
        description="Directory holding per-provider YAML config files",
    )
    verbose: bool = Field(default=False, description="Enable verbose output")

    server: ServerConfig = Field(default_factory=ServerConfig)
    logging: LoggingConfig = Field(default_factory=LoggingConfig)
    cache: CacheConfig = Field(default_factory=CacheConfig)

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# YAML helper
# ---------------------------------------------------------------------------

def _load_yaml(path: Path) -> dict:
    """Read and return the contents of a YAML file as a dict.

    Returns an empty dict if the file does not exist or is empty.
    Raises ``ValueError`` on malformed YAML.
    """
    if not path.exists():
        return {}

    raw = path.read_text(encoding="utf-8").strip()
    if not raw:
        return {}

    try:
        data = yaml.safe_load(raw)
    except yaml.YAMLError as exc:
        raise ValueError(f"Malformed YAML in {path}: {exc}") from exc

    if data is None:
        return {}
    if not isinstance(data, dict):
        raise ValueError(f"Expected a mapping (dict) in {path}, got {type(data).__name__}")

    return data


# ---------------------------------------------------------------------------
# Env-var helper
# ---------------------------------------------------------------------------

def _env_value(key: str, override: Optional[str]) -> Optional[str]:
    """Return *override* if provided, else look up ``LLM_PROXY_<KEY>``."""
    if override is not None:
        return override
    env_key = f"{ENV_PREFIX}{key.upper()}"
    return os.environ.get(env_key)


def _apply_env_overrides(cfg: Config) -> Config:
    """Apply ``LLM_PROXY_*`` env vars on top of a (defaults + YAML) config.

    Nested fields use ``__`` as delimiter, e.g.:
      ``LLM_PROXY_SERVER__PORT=3000`` -> ``cfg.server.port = 3000``
    """
    update_data: dict = {}

    def _maybe_set(target_path: str, env_suffix: str, field_type: type):
        raw_val = os.environ.get(f"{ENV_PREFIX}{env_suffix}")
        if raw_val is None:
            return
        # Cast to the target field type
        parts = target_path.split(".")
        try:
            typed_val = _coerce(raw_val, field_type)
        except (ValueError, TypeError):
            print(f"Warning: {ENV_PREFIX}{env_suffix}={raw_val!r} is not a valid {field_type.__name__}, skipping.", file=sys.stderr)
            return
        d = update_data
        for p in parts[:-1]:
            d = d.setdefault(p, {})
        d[parts[-1]] = typed_val

    def _coerce(val: str, typ: type):
        """Convert a string env-var value to the target Python type."""
        if typ is bool:
            return val.lower() in ("1", "true", "yes", "on")
        if typ is int:
            return int(val)
        if typ is str:
            return val
        return val

    # Scalar fields
    _maybe_set("default_provider", "DEFAULT_PROVIDER", str)
    _maybe_set("providers_dir", "PROVIDERS_DIR", str)
    _maybe_set("verbose", "VERBOSE", bool)

    # Nested: server.*
    _maybe_set("server.host", "SERVER__HOST", str)
    _maybe_set("server.port", "SERVER__PORT", int)
    _maybe_set("server.workers", "SERVER__WORKERS", int)
    _maybe_set("server.reload", "SERVER__RELOAD", bool)

    # Nested: logging.*
    _maybe_set("logging.level", "LOGGING__LEVEL", str)
    _maybe_set("logging.format", "LOGGING__FORMAT", str)

    # Nested: cache.*
    _maybe_set("cache.backend", "CACHE__BACKEND", str)
    _maybe_set("cache.path", "CACHE__PATH", str)
    _maybe_set("cache.ttl_seconds", "CACHE__TTL_SECONDS", int)

    if update_data:
        # Deep-merge update_data into cfg's dict representation
        cfg_dict = cfg.model_dump()
        _deep_merge(cfg_dict, update_data)
        cfg = Config(**cfg_dict)

    return cfg


def _deep_merge(base: dict, override: dict) -> None:
    """Merge *override* into *base* in-place, recursing for nested dicts."""
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            _deep_merge(base[key], val)
        else:
            base[key] = val


# ---------------------------------------------------------------------------
# Config loader (cached)
# ---------------------------------------------------------------------------

_settings: Optional[Config] = None


def load_config(path: Path | str | None = None, *, reload: bool = False) -> Config:
    """Load (or reload) global proxy configuration.

    Parameters
    ----------
    path :
        Explicit YAML path.  Defaults to ``~/.llm-proxy/config.yaml``.
    reload :
        If ``True``, bypass the module-level cache and re-read from disk.

    Returns
    -------
    Config
        Validated configuration with YAML + env overrides applied in
        correct priority order: defaults < YAML < env vars.
    """
    global _settings

    if not reload and _settings is not None:
        return _settings

    filepath = Path(path) if path else CONFIG_FILE
    yaml_data = _load_yaml(filepath)

    try:
        # Step 1: defaults + YAML (kwargs override pydantic defaults)
        cfg = Config(**yaml_data)
    except ValidationError as exc:
        print(f"Config validation error: {exc}", file=sys.stderr)
        sys.exit(1)

    # Step 2: env vars win over everything
    cfg = _apply_env_overrides(cfg)

    _settings = cfg
    return cfg


def ensure_config_dir() -> Path:
    """Create ``~/.llm-proxy/`` (and its ``providers/`` subdirectory) if
    they do not exist.  Returns the config directory path."""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    providers_path = CONFIG_DIR / "providers"
    providers_path.mkdir(parents=True, exist_ok=True)
    return CONFIG_DIR


# ---------------------------------------------------------------------------
# Convenience accessor
# ---------------------------------------------------------------------------

def get_config() -> Config:
    """Return the cached global config, loading it on first call."""
    global _settings
    if _settings is None:
        return load_config()
    return _settings


def reset_config() -> None:
    """Clear the module-level cached config (useful for testing)."""
    global _settings
    _settings = None


# ---------------------------------------------------------------------------
# CLI entry guard
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    cfg = load_config(reload=True)
    print(cfg.model_dump_json(indent=2))
