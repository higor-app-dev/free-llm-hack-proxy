"""Per-provider configuration loader.

Loads, validates, and caches individual provider YAML files from the
``providers/`` directory (default: ``~/.llm-proxy/providers/``).

Each file is a single provider definition conforming to
:class:`src.providers.models.ProviderConfig`.

Usage::

    from src.providers.config import load_provider_config, list_providers

    openai_cfg = load_provider_config("openai")
    print(openai_cfg.base_url)

    for name in list_providers():
        cfg = load_provider_config(name)
        print(cfg.name, len(cfg.models), "models")
"""

from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import Optional

from src.config import CONFIG_DIR
from src.providers.models import ProviderConfig

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Default providers directory
# ---------------------------------------------------------------------------

DEFAULT_PROVIDERS_DIR = CONFIG_DIR / "providers"

# ---------------------------------------------------------------------------
# Module-level cache: provider_name -> ProviderConfig
# ---------------------------------------------------------------------------

_provider_cache: dict[str, ProviderConfig] = {}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _providers_dir() -> Path:
    """Return the path to the per-provider config directory.

    Respects the ``providers_dir`` setting from the global config, falling
    back to the default ``~/.llm-proxy/providers/``.
    """
    # Late import to avoid circular dependency on first load
    from src.config import get_config

    try:
        cfg = get_config()
        return Path(cfg.providers_dir)
    except Exception:
        return DEFAULT_PROVIDERS_DIR


def _provider_path(name: str) -> Path:
    """Return the expected YAML path for provider *name*."""
    return _providers_dir() / f"{name}.yaml"


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

def load_provider_config(
    name: str,
    *,
    reload: bool = False,
    path: Optional[Path | str] = None,
) -> ProviderConfig:
    """Load (or reload) a single provider's configuration by name.

    Parameters
    ----------
    name :
        Provider identifier (e.g. ``openai``, ``anthropic``, ``groq``).
    reload :
        If ``True``, bypass the module-level cache and re-read from disk.
    path :
        Explicit path override.  Defaults to ``<providers_dir>/<name>.yaml``.

    Returns
    -------
    ProviderConfig
        Validated provider configuration.

    Raises
    ------
    FileNotFoundError
        If the YAML file does not exist.
    ValueError
        If the YAML is malformed or fails validation.
    """
    name = name.strip().lower()

    if not reload and name in _provider_cache:
        return _provider_cache[name]

    filepath = Path(path) if path else _provider_path(name)

    cfg = ProviderConfig.from_yaml(filepath)

    # Verify that the name in the file matches the requested name (defensive)
    if cfg.name != name:
        logger.warning(
            "Provider file %s declares name=%r, but was loaded as %r",
            filepath,
            cfg.name,
            name,
        )

    _provider_cache[name] = cfg
    return cfg


def load_all_providers(*, reload: bool = False) -> dict[str, ProviderConfig]:
    """Load every provider YAML file found in the providers directory.

    Invalid files are skipped and logged as warnings rather than failing
    the entire batch load.

    Returns
    -------
    dict[str, ProviderConfig]
        Mapping of provider name -> validated config (one per YAML file).
    """
    prov_dir = _providers_dir()
    if not prov_dir.is_dir():
        logger.info("Providers directory %s does not exist yet", prov_dir)
        return {}

    result: dict[str, ProviderConfig] = {}
    for yaml_path in sorted(prov_dir.glob("*.yaml")):
        name_hint = yaml_path.stem
        try:
            cfg = load_provider_config(name_hint, reload=reload)
            result[cfg.name] = cfg
        except (FileNotFoundError, ValueError) as exc:
            logger.warning("Skipping %s: %s", yaml_path, exc)

    return result


def list_providers() -> list[str]:
    """Return sorted list of available provider names.

    Scans the providers directory for ``*.yaml`` files and returns their
    stem names.  Does **not** attempt to parse or validate the files.
    """
    prov_dir = _providers_dir()
    if not prov_dir.is_dir():
        return []
    return sorted(p.stem for p in prov_dir.glob("*.yaml"))


def reset_provider_cache() -> None:
    """Clear the module-level provider config cache (useful for testing)."""
    _provider_cache.clear()


def ensure_providers_dir() -> Path:
    """Create the providers directory if it doesn't exist.  Returns its path."""
    prov_dir = _providers_dir()
    prov_dir.mkdir(parents=True, exist_ok=True)
    return prov_dir


# ---------------------------------------------------------------------------
# Env-var helper
# ---------------------------------------------------------------------------

def provider_api_key(name: str) -> Optional[str]:
    """Resolve the API key for provider *name*.

    Tries in order:
      1. The ``api_key`` field from the provider's YAML config.
      2. The env var ``LLM_PROXY_<NAME>_API_KEY`` (uppercased name).
    Returns ``None`` if neither is set.

    Always reloads from disk to pick up YAML edits and env-var changes
    that happened after the provider config was first loaded.
    """
    try:
        cfg = load_provider_config(name, reload=True)
        key = cfg.resolve_api_key()
        if key:
            return key
    except (FileNotFoundError, ValueError):
        pass

    # Fallback to env var even without a config file
    env_key = f"LLM_PROXY_{name.upper().replace('-', '_')}_API_KEY"
    return os.environ.get(env_key)
