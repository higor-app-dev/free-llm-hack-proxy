"""Tests for ``src.config`` — global configuration loading."""

from __future__ import annotations

import tempfile
from pathlib import Path

import pytest
import yaml

from src.config import (
    CONFIG_DIR,
    Config,
    ServerConfig,
    LoggingConfig,
    CacheConfig,
    load_config,
    ensure_config_dir,
    get_config,
    reset_config,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture
def tmp_config_dir():
    """Yield a temporary directory that simulates ``~/.llm-proxy``."""
    with tempfile.TemporaryDirectory() as td:
        yield Path(td)


@pytest.fixture
def write_yaml(tmp_config_dir):
    """Helper that writes a YAML file inside the temporary config dir."""

    def _write(data: dict, filename: str = "config.yaml") -> Path:
        path = tmp_config_dir / filename
        path.write_text(yaml.dump(data), encoding="utf-8")
        return path

    return _write


# ---------------------------------------------------------------------------
# Unit: model defaults
# ---------------------------------------------------------------------------

class TestConfigDefaults:
    """Hard-coded defaults must be correct."""

    def test_server_defaults(self):
        s = ServerConfig()
        assert s.host == "0.0.0.0"
        assert s.port == 8080
        assert s.workers == 1
        assert s.reload is False

    def test_logging_defaults(self):
        lc = LoggingConfig()
        assert lc.level == "INFO"
        assert "%(asctime)s" in lc.format

    def test_cache_defaults(self):
        c = CacheConfig()
        assert c.backend == "sqlite"
        assert c.path == str(CONFIG_DIR / "cache.db")
        assert c.ttl_seconds == 300

    def test_top_level_defaults(self):
        cfg = Config()
        assert cfg.default_provider == "auto"
        assert cfg.providers_dir == str(CONFIG_DIR / "providers")
        assert cfg.verbose is False
        assert isinstance(cfg.server, ServerConfig)
        assert isinstance(cfg.logging, LoggingConfig)
        assert isinstance(cfg.cache, CacheConfig)


# ---------------------------------------------------------------------------
# Unit: validation
# ---------------------------------------------------------------------------

class TestConfigValidation:
    """Pydantic validators must catch bad values."""

    def test_port_out_of_range(self):
        with pytest.raises(Exception):
            ServerConfig(port=99999)

    def test_port_zero(self):
        with pytest.raises(Exception):
            ServerConfig(port=0)

    def test_workers_zero(self):
        with pytest.raises(Exception):
            ServerConfig(workers=0)

    def test_bad_log_level(self):
        with pytest.raises(Exception):
            LoggingConfig(level="TRACE")

    def test_bad_cache_backend(self):
        with pytest.raises(Exception):
            CacheConfig(backend="memcached")

    def test_extra_field_rejected(self):
        with pytest.raises(Exception):
            Config(nonexistent_key="oops")


# ---------------------------------------------------------------------------
# Integration: YAML loading
# ---------------------------------------------------------------------------

class TestYamlLoading:
    """Config loads correctly from YAML files."""

    def test_yaml_overrides_defaults(self, write_yaml):
        path = write_yaml({
            "default_provider": "groq",
            "verbose": True,
            "server": {"port": 9090, "workers": 4},
            "logging": {"level": "DEBUG"},
            "cache": {"ttl_seconds": 600},
        })
        cfg = load_config(path, reload=True)

        # Overridden values
        assert cfg.default_provider == "groq"
        assert cfg.verbose is True
        assert cfg.server.port == 9090
        assert cfg.server.workers == 4
        assert cfg.logging.level == "DEBUG"
        assert cfg.cache.ttl_seconds == 600

        # Defaults that weren't touched
        assert cfg.server.host == "0.0.0.0"
        assert cfg.cache.backend == "sqlite"
        assert cfg.logging.format == LoggingConfig().format

    def test_empty_yaml_uses_defaults(self, tmp_config_dir):
        path = tmp_config_dir / "empty.yaml"
        path.write_text("", encoding="utf-8")
        cfg = load_config(path, reload=True)
        assert cfg.default_provider == "auto"
        assert cfg.server.port == 8080

    def test_missing_file_uses_defaults(self):
        cfg = load_config("/nonexistent/path/config.yaml", reload=True)
        assert cfg.default_provider == "auto"

    def test_malformed_yaml_raises(self, write_yaml):
        path = write_yaml({"valid": "data"})
        path.write_text("{invalid: [broken", encoding="utf-8")
        with pytest.raises(ValueError, match="Malformed YAML"):
            load_config(path, reload=True)

    def test_partial_yaml_keeps_remaining_defaults(self, write_yaml):
        """Only set one key — everything else stays default."""
        path = write_yaml({"default_provider": "openai"})
        cfg = load_config(path, reload=True)
        assert cfg.default_provider == "openai"
        assert cfg.server.port == 8080
        assert cfg.logging.level == "INFO"
        assert cfg.cache.backend == "sqlite"


# ---------------------------------------------------------------------------
# Integration: env var overrides
# ---------------------------------------------------------------------------

class TestEnvVarOverrides:
    """Environment variables must override both defaults and YAML values."""

    def test_env_overrides_default(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_DEFAULT_PROVIDER", "anthropic")
        monkeypatch.setenv("LLM_PROXY_VERBOSE", "true")
        cfg = load_config("/dev/null", reload=True)
        assert cfg.default_provider == "anthropic"
        assert cfg.verbose is True

    def test_env_overrides_yaml(self, monkeypatch, write_yaml):
        """Env var wins even when YAML has a different value."""
        path = write_yaml({"default_provider": "groq"})
        monkeypatch.setenv("LLM_PROXY_DEFAULT_PROVIDER", "huggingface")
        cfg = load_config(path, reload=True)
        assert cfg.default_provider == "huggingface"  # env beats YAML

    def test_env_nested_server_port(self, monkeypatch, write_yaml):
        path = write_yaml({"server": {"port": 9090}})
        monkeypatch.setenv("LLM_PROXY_SERVER__PORT", "3000")
        cfg = load_config(path, reload=True)
        assert cfg.server.port == 3000  # env beats YAML

    def test_env_nested_logging_level(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_LOGGING__LEVEL", "DEBUG")
        cfg = load_config("/dev/null", reload=True)
        assert cfg.logging.level == "DEBUG"

    def test_env_bool_false(self, monkeypatch, write_yaml):
        """``LLM_PROXY_VERBOSE=false`` should override YAML ``verbose: true``."""
        path = write_yaml({"verbose": True})
        monkeypatch.setenv("LLM_PROXY_VERBOSE", "false")
        cfg = load_config(path, reload=True)
        assert cfg.verbose is False

    def test_env_bool_true_via_1(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_VERBOSE", "1")
        cfg = load_config("/dev/null", reload=True)
        assert cfg.verbose is True

    def test_env_int_override(self, monkeypatch, write_yaml):
        monkeypatch.setenv("LLM_PROXY_CACHE__TTL_SECONDS", "3600")
        cfg = load_config("/dev/null", reload=True)
        assert cfg.cache.ttl_seconds == 3600


# ---------------------------------------------------------------------------
# Integration: ensure_config_dir
# ---------------------------------------------------------------------------

class TestEnsureConfigDir:
    def test_creates_config_dir(self, monkeypatch, tmp_config_dir):
        """Should create both ``~/.llm-proxy`` and ``providers/``."""
        monkeypatch.setattr("src.config.CONFIG_DIR", tmp_config_dir)
        result = ensure_config_dir()
        assert result == tmp_config_dir
        assert tmp_config_dir.exists()
        assert (tmp_config_dir / "providers").exists()

    def test_idempotent(self, monkeypatch, tmp_config_dir):
        """Calling twice should not raise."""
        monkeypatch.setattr("src.config.CONFIG_DIR", tmp_config_dir)
        ensure_config_dir()
        ensure_config_dir()  # second call must not fail


# ---------------------------------------------------------------------------
# Integration: caching
# ---------------------------------------------------------------------------

class TestCaching:
    def test_get_config_caches(self, monkeypatch):
        reset_config()  # clear state from prior tests
        monkeypatch.setenv("LLM_PROXY_DEFAULT_PROVIDER", "cache-test")
        get_config()
        monkeypatch.delenv("LLM_PROXY_DEFAULT_PROVIDER")
        second = get_config()
        # second call returns the cached first result, not a fresh load
        assert second.default_provider == "cache-test"

    def test_reload_bypasses_cache(self, monkeypatch, write_yaml):
        reset_config()
        path = write_yaml({"default_provider": "v1"})
        cfg1 = load_config(path, reload=True)
        assert cfg1.default_provider == "v1"

        write_yaml({"default_provider": "v2"})
        cfg2 = load_config(path, reload=True)
        assert cfg2.default_provider == "v2"
