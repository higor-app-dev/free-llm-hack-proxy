"""Tests for ``src.providers`` — per-provider YAML config loading."""

from __future__ import annotations

import tempfile
from pathlib import Path

import pytest
import yaml

from src.providers import (
    ModelConfig,
    ProviderConfig,
    RateLimitConfig,
    list_providers,
    load_all_providers,
    load_provider_config,
    provider_api_key,
    reset_provider_cache,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture
def tmp_providers_dir():
    """Yield a temporary directory for provider YAML files."""
    with tempfile.TemporaryDirectory() as td:
        yield Path(td)


@pytest.fixture
def write_provider(tmp_providers_dir):
    """Helper that writes a provider YAML file inside the temp dir."""

    def _write(name: str, data: dict) -> Path:
        path = tmp_providers_dir / f"{name}.yaml"
        path.write_text(yaml.dump(data), encoding="utf-8")
        return path

    return _write


# ---------------------------------------------------------------------------
# Unit: model defaults and validation
# ---------------------------------------------------------------------------

class TestModelConfig:
    """Unit tests for ModelConfig pydantic model."""

    def test_minimal_model(self):
        m = ModelConfig(id="gpt-4o")
        assert m.id == "gpt-4o"
        assert m.max_tokens == 4096
        assert m.supports_streaming is True
        assert m.supports_vision is False
        assert m.supports_functions is False

    def test_full_model(self):
        m = ModelConfig(
            id="claude-sonnet-4",
            max_tokens=200000,
            supports_streaming=True,
            supports_vision=True,
            supports_functions=True,
            cost_per_1k_input=0.003,
            cost_per_1k_output=0.015,
        )
        assert m.max_tokens == 200000
        assert m.cost_per_1k_input == 0.003

    def test_max_tokens_positive(self):
        with pytest.raises(Exception):
            ModelConfig(id="bad", max_tokens=0)

    def test_extra_field_rejected(self):
        with pytest.raises(Exception):
            ModelConfig(id="x", unknown_field="oops")


class TestRateLimitConfig:
    """Unit tests for RateLimitConfig pydantic model."""

    def test_minimal(self):
        rl = RateLimitConfig()
        assert rl.requests_per_minute == 60
        assert rl.tokens_per_minute is None
        assert rl.requests_per_day is None

    def test_custom(self):
        rl = RateLimitConfig(requests_per_minute=10, tokens_per_minute=5000)
        assert rl.requests_per_minute == 10
        assert rl.tokens_per_minute == 5000

    def test_extra_field_rejected(self):
        with pytest.raises(Exception):
            RateLimitConfig(unknown="x")


class TestProviderConfig:
    """Unit tests for ProviderConfig pydantic model."""

    def test_minimal_required(self):
        p = ProviderConfig(name="openai")
        assert p.name == "openai"
        assert p.api_key is None
        assert p.base_url is None
        assert p.models == []
        assert p.timeout_seconds == 30

    def test_name_normalized(self):
        p = ProviderConfig(name="  Openai-Test  ")
        assert p.name == "openai-test"

    def test_empty_name_rejected(self):
        with pytest.raises(Exception, match="must not be empty"):
            ProviderConfig(name="  ")

    def test_invalid_chars_in_name(self):
        with pytest.raises(Exception, match="invalid characters"):
            ProviderConfig(name="hello world!")

    def test_timeout_range(self):
        with pytest.raises(Exception):
            ProviderConfig(name="x", timeout_seconds=0)
        with pytest.raises(Exception):
            ProviderConfig(name="x", timeout_seconds=301)

    def test_full_config(self):
        p = ProviderConfig(
            name="test-provider",
            api_key="sk-secret",
            base_url="https://api.test.com/v1",
            timeout_seconds=60,
            models=[ModelConfig(id="model-a"), ModelConfig(id="model-b")],
            rate_limits=RateLimitConfig(requests_per_minute=100),
            extra_headers={"X-Custom": "value"},
            description="Test provider",
        )
        assert len(p.models) == 2
        assert p.rate_limits.requests_per_minute == 100
        assert p.extra_headers["X-Custom"] == "value"

    def test_extra_field_rejected(self):
        with pytest.raises(Exception):
            ProviderConfig(name="x", unknown_key="bad")


# ---------------------------------------------------------------------------
# Unit: ProviderConfig.from_yaml()
# ---------------------------------------------------------------------------

class TestProviderConfigFromYaml:
    """Tests for parsing YAML into ProviderConfig."""

    def test_parse_valid(self, write_provider):
        path = write_provider("openai", {
            "name": "openai",
            "base_url": "https://api.openai.com/v1",
            "models": [{"id": "gpt-4o", "max_tokens": 128000}],
        })
        cfg = ProviderConfig.from_yaml(path)
        assert cfg.name == "openai"
        assert cfg.base_url == "https://api.openai.com/v1"
        assert len(cfg.models) == 1
        assert cfg.models[0].id == "gpt-4o"

    def test_missing_file(self):
        with pytest.raises(FileNotFoundError, match="not found"):
            ProviderConfig.from_yaml("/nonexistent/openai.yaml")

    def test_empty_file(self, tmp_providers_dir):
        path = tmp_providers_dir / "empty.yaml"
        path.write_text("", encoding="utf-8")
        with pytest.raises(ValueError, match="empty"):
            ProviderConfig.from_yaml(path)

    def test_null_document(self, tmp_providers_dir):
        path = tmp_providers_dir / "null.yaml"
        path.write_text("~\n", encoding="utf-8")
        with pytest.raises(ValueError, match="null"):
            ProviderConfig.from_yaml(path)

    def test_malformed_yaml(self, tmp_providers_dir):
        path = tmp_providers_dir / "bad.yaml"
        path.write_text("{invalid: [broken", encoding="utf-8")
        with pytest.raises(ValueError, match="Malformed YAML"):
            ProviderConfig.from_yaml(path)

    def test_not_a_dict(self, tmp_providers_dir):
        path = tmp_providers_dir / "list.yaml"
        path.write_text(yaml.dump(["a", "b"]), encoding="utf-8")
        with pytest.raises(ValueError, match="list"):
            ProviderConfig.from_yaml(path)


# ---------------------------------------------------------------------------
# Unit: ProviderConfig.resolve_api_key()
# ---------------------------------------------------------------------------

class TestResolveApiKey:
    """Tests for API key resolution (YAML -> env var)."""

    def test_from_yaml(self):
        p = ProviderConfig(name="openai", api_key="sk-from-yaml")
        assert p.resolve_api_key() == "sk-from-yaml"

    def test_from_env(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_OPENAI_API_KEY", "sk-from-env")
        p = ProviderConfig(name="openai")  # no api_key in YAML
        assert p.resolve_api_key() == "sk-from-env"

    def test_yaml_beats_env(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_OPENAI_API_KEY", "sk-env")
        p = ProviderConfig(name="openai", api_key="sk-yaml")
        assert p.resolve_api_key() == "sk-yaml"

    def test_none_when_unset(self):
        p = ProviderConfig(name="openai")
        assert p.resolve_api_key() is None

    def test_hyphenated_name_env(self, monkeypatch):
        monkeypatch.setenv("LLM_PROXY_HUGGINGFACE_API_KEY", "sk-hf")
        p = ProviderConfig(name="huggingface")
        assert p.resolve_api_key() == "sk-hf"


# ---------------------------------------------------------------------------
# Unit: ProviderConfig.get_model()
# ---------------------------------------------------------------------------

class TestGetModel:
    """Tests for looking up a model by ID."""

    def test_found(self):
        p = ProviderConfig(
            name="test",
            models=[ModelConfig(id="a"), ModelConfig(id="b")],
        )
        assert p.get_model("a") is not None
        assert p.get_model("a").id == "a"

    def test_not_found(self):
        p = ProviderConfig(name="test", models=[ModelConfig(id="a")])
        assert p.get_model("nonexistent") is None

    def test_empty_models(self):
        p = ProviderConfig(name="test")
        assert p.get_model("anything") is None


# ---------------------------------------------------------------------------
# Integration: load_provider_config()
# ---------------------------------------------------------------------------

class TestLoadProviderConfig:
    """Tests for the module-level config loader function."""

    @pytest.fixture(autouse=True)
    def clear_cache(self):
        reset_provider_cache()

    @pytest.fixture
    def patch_providers_dir(self, tmp_providers_dir, monkeypatch):
        monkeypatch.setattr("src.providers.config._providers_dir", lambda: tmp_providers_dir)

    def test_load_valid(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        path = tmp_providers_dir / "openai.yaml"
        path.write_text(yaml.dump({
            "name": "openai",
            "base_url": "https://api.openai.com/v1",
        }), encoding="utf-8")

        cfg = load_provider_config("openai")
        assert cfg.name == "openai"
        assert cfg.base_url == "https://api.openai.com/v1"

    def test_missing_raises(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        with pytest.raises(FileNotFoundError):
            load_provider_config("nonexistent")

    def test_malformed_raises(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        path = tmp_providers_dir / "openai.yaml"
        path.write_text("{broken yaml", encoding="utf-8")
        with pytest.raises(ValueError, match="Malformed YAML"):
            load_provider_config("openai")

    def test_case_insensitive(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        path = tmp_providers_dir / "openai.yaml"
        path.write_text(yaml.dump({"name": "openai"}), encoding="utf-8")

        cfg = load_provider_config("OpenAI")
        assert cfg.name == "openai"

    def test_reload_bypasses_cache(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        path = tmp_providers_dir / "openai.yaml"

        # Write v1
        path.write_text(yaml.dump({"name": "openai", "base_url": "v1"}), encoding="utf-8")
        cfg1 = load_provider_config("openai", reload=True)
        assert cfg1.base_url == "v1"

        # Write v2
        path.write_text(yaml.dump({"name": "openai", "base_url": "v2"}), encoding="utf-8")
        cfg2 = load_provider_config("openai", reload=True)
        assert cfg2.base_url == "v2"

    def test_cache_serves_without_reload(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        path = tmp_providers_dir / "openai.yaml"
        path.write_text(yaml.dump({"name": "openai", "base_url": "v1"}), encoding="utf-8")

        reset_provider_cache()
        cfg1 = load_provider_config("openai")
        assert cfg1.base_url == "v1"

        # Change file but don't reload — cache should return v1
        path.write_text(yaml.dump({"name": "openai", "base_url": "v2"}), encoding="utf-8")
        cfg2 = load_provider_config("openai")  # no reload
        assert cfg2.base_url == "v1"  # cached


# ---------------------------------------------------------------------------
# Integration: load_all_providers()
# ---------------------------------------------------------------------------

class TestLoadAllProviders:
    """Tests for bulk-loading all provider configs."""

    @pytest.fixture(autouse=True)
    def clear_cache(self):
        reset_provider_cache()

    @pytest.fixture
    def patch_providers_dir(self, tmp_providers_dir, monkeypatch):
        monkeypatch.setattr("src.providers.config._providers_dir", lambda: tmp_providers_dir)

    def test_load_all(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        (tmp_providers_dir / "openai.yaml").write_text(
            yaml.dump({"name": "openai", "base_url": "https://openai.com"}), encoding="utf-8")
        (tmp_providers_dir / "anthropic.yaml").write_text(
            yaml.dump({"name": "anthropic", "base_url": "https://anthropic.com"}), encoding="utf-8")

        result = load_all_providers(reload=True)
        assert "openai" in result
        assert "anthropic" in result
        assert result["openai"].base_url == "https://openai.com"

    def test_skip_missing_directory(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        # Directory exists but is empty
        result = load_all_providers()
        assert result == {}

    def test_skip_corrupted_file(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        (tmp_providers_dir / "good.yaml").write_text(
            yaml.dump({"name": "good"}), encoding="utf-8")
        (tmp_providers_dir / "bad.yaml").write_text(
            "{invalid", encoding="utf-8")

        result = load_all_providers(reload=True)
        assert "good" in result
        assert "bad" not in result  # skipped with warning


# ---------------------------------------------------------------------------
# Integration: list_providers()
# ---------------------------------------------------------------------------

class TestListProviders:
    @pytest.fixture
    def patch_providers_dir(self, tmp_providers_dir, monkeypatch):
        monkeypatch.setattr("src.providers.config._providers_dir", lambda: tmp_providers_dir)

    def test_returns_stems(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        (tmp_providers_dir / "openai.yaml").write_text("name: openai\n", encoding="utf-8")
        (tmp_providers_dir / "anthropic.yaml").write_text("name: anthropic\n", encoding="utf-8")

        names = list_providers()
        assert names == ["anthropic", "openai"]  # sorted

    def test_empty_dir(self, tmp_providers_dir, monkeypatch, patch_providers_dir):
        assert list_providers() == []

    def test_non_existent_dir(self, tmp_providers_dir, monkeypatch):
        """Should return empty list, not raise."""
        nonexistent = tmp_providers_dir / "does_not_exist"
        monkeypatch.setattr("src.providers.config._providers_dir", lambda: nonexistent)
        assert list_providers() == []


# ---------------------------------------------------------------------------
# Integration: provider_api_key()
# ---------------------------------------------------------------------------

class TestProviderApiKey:
    """Tests for the top-level API key resolution helper."""

    def test_from_env_fallback(self, monkeypatch, tmp_providers_dir):
        monkeypatch.setattr("src.providers.config.DEFAULT_PROVIDERS_DIR", tmp_providers_dir)
        monkeypatch.setenv("LLM_PROXY_GROQ_API_KEY", "gsk-env")
        # No YAML file exists for groq
        key = provider_api_key("groq")
        assert key == "gsk-env"

    def test_from_yaml_wins(self, tmp_providers_dir, monkeypatch):
        monkeypatch.setattr("src.providers.config._providers_dir", lambda: tmp_providers_dir)
        (tmp_providers_dir / "groq.yaml").write_text(
            yaml.dump({"name": "groq", "api_key": "gsk-yaml"}), encoding="utf-8")
        monkeypatch.setenv("LLM_PROXY_GROQ_API_KEY", "gsk-env")

        key = provider_api_key("groq")
        assert key == "gsk-yaml"

    def test_no_key(self, tmp_providers_dir, monkeypatch):
        monkeypatch.setattr("src.providers.config.DEFAULT_PROVIDERS_DIR", tmp_providers_dir)
        assert provider_api_key("nonexistent") is None


# ---------------------------------------------------------------------------
# Integration: example files parse correctly
# ---------------------------------------------------------------------------

class TestExampleFiles:
    """Verify that the shipped example YAML files are valid."""

    EXAMPLE_DIR = Path(__file__).parent.parent / "examples" / "providers"

    def test_all_examples_are_valid(self):
        if not self.EXAMPLE_DIR.is_dir():
            pytest.skip("examples/providers directory not found")
        for yaml_path in sorted(self.EXAMPLE_DIR.glob("*.yaml")):
            try:
                cfg = ProviderConfig.from_yaml(yaml_path)
            except Exception as exc:
                pytest.fail(f"Failed to parse {yaml_path.name}: {exc}")
            assert cfg.name == yaml_path.stem, (
                f"File {yaml_path.name} declares name={cfg.name!r}, "
                f"expected {yaml_path.stem!r}"
            )

    def test_example_roundtrip(self, tmp_providers_dir):
        """Read an example file, dump it back, re-read, ensure same data."""
        if not self.EXAMPLE_DIR.is_dir():
            pytest.skip("examples/providers directory not found")
        src = self.EXAMPLE_DIR / "openai.yaml"
        if not src.exists():
            pytest.skip("openai.yaml example missing")

        cfg1 = ProviderConfig.from_yaml(src)
        roundtrip_path = tmp_providers_dir / "roundtrip.yaml"
        cfg1.to_yaml(roundtrip_path)
        cfg2 = ProviderConfig.from_yaml(roundtrip_path)

        # Core fields must match
        assert cfg2.name == cfg1.name
        assert cfg2.base_url == cfg1.base_url
        assert len(cfg2.models) == len(cfg1.models)
