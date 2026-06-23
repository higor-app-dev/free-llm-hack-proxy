"""Pydantic models for per-provider configuration.

Each provider lives in its own YAML file under ``~/.llm-proxy/providers/``.
This module defines the schema those YAML files must conform to.

Usage::

    from src.providers.models import ProviderConfig

    cfg = ProviderConfig.from_yaml("~/path/to/openai.yaml")
    print(cfg.name, cfg.base_url)
"""

from __future__ import annotations

from pathlib import Path
from typing import Optional

import yaml
from pydantic import BaseModel, Field, field_validator


# ---------------------------------------------------------------------------
# ModelConfig — a single LLM model entry within a provider
# ---------------------------------------------------------------------------

class ModelConfig(BaseModel):
    """Describes one LLM model exposed by a provider."""

    id: str = Field(..., description="Model identifier (e.g. 'gpt-4o', 'claude-sonnet-4')")
    max_tokens: int = Field(default=4096, ge=1, description="Maximum context length in tokens")
    supports_streaming: bool = Field(default=True, description="Whether the model supports streaming")
    supports_vision: bool = Field(default=False, description="Whether the model supports image inputs")
    supports_functions: bool = Field(default=False, description="Whether the model supports tool calls")
    cost_per_1k_input: Optional[float] = Field(default=None, ge=0, description="Cost per 1K input tokens (USD)")
    cost_per_1k_output: Optional[float] = Field(default=None, ge=0, description="Cost per 1K output tokens (USD)")

    model_config = {"extra": "forbid"}

    def __str__(self) -> str:
        return f"{self.id} (max_tokens={self.max_tokens}, streaming={self.supports_streaming})"


# ---------------------------------------------------------------------------
# RateLimitConfig — optional rate-limit constraints
# ---------------------------------------------------------------------------

class RateLimitConfig(BaseModel):
    """Rate-limit settings applied to this provider's API."""

    requests_per_minute: int = Field(default=60, ge=1, description="Max requests per minute")
    tokens_per_minute: Optional[int] = Field(default=None, ge=1, description="Max tokens per minute")
    requests_per_day: Optional[int] = Field(default=None, ge=1, description="Max requests per day")

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# ProviderConfig — top-level per-provider configuration
# ---------------------------------------------------------------------------

DEFAULT_TIMEOUT = 30


class ProviderConfig(BaseModel):
    """Schema for a single provider YAML file.

    Fields
    ------
    name :
        Unique provider identifier (e.g. ``openai``, ``anthropic``, ``groq``).
    api_key :
        Authentication key.  May be left blank if provided via env var
        (``LLM_PROXY_<PROVIDER>_API_KEY``).
    base_url :
        Base endpoint URL.  If omitted, uses the provider's well-known URL
        (determined at runtime by the provider implementation).
    models :
        List of models this provider serves.
    timeout_seconds :
        HTTP request timeout in seconds.
    rate_limits :
        Optional throttling constraints.
    extra_headers :
        Optional additional HTTP headers to send with every request.
    description :
        Human-readable label shown in the CLI.
    """

    name: str = Field(..., description="Unique provider identifier")
    api_key: Optional[str] = Field(default=None, description="API key (or set via env var)")
    base_url: Optional[str] = Field(default=None, description="API endpoint URL")
    models: list[ModelConfig] = Field(default_factory=list, description="Available models")
    timeout_seconds: int = Field(default=DEFAULT_TIMEOUT, ge=1, le=300, description="Request timeout")
    rate_limits: Optional[RateLimitConfig] = Field(default=None, description="Rate-limit settings")
    extra_headers: Optional[dict[str, str]] = Field(default=None, description="Extra HTTP headers")
    description: str = Field(default="", description="Human-readable label")

    model_config = {"extra": "forbid"}

    @field_validator("name", mode="before")
    @classmethod
    def name_must_be_slug(cls, v: str) -> str:
        if not isinstance(v, str):
            raise ValueError(f"Provider name must be a string, got {type(v).__name__}")
        stripped = v.strip()
        if not stripped:
            raise ValueError("Provider name must not be empty")
        if not stripped.replace("-", "").replace("_", "").isalnum():
            raise ValueError(
                f"Provider name {v!r} contains invalid characters. "
                "Use only letters, digits, hyphens, and underscores."
            )
        return stripped.lower()

    # ------------------------------------------------------------------
    # YAML helpers
    # ------------------------------------------------------------------

    @classmethod
    def from_yaml(cls, path: Path | str) -> ProviderConfig:
        """Parse *path* and return a validated ``ProviderConfig``.

        Raises
        ------
        FileNotFoundError
            If the file does not exist.
        ValueError
            If the YAML is malformed or the structure is invalid.
        """
        p = Path(path)
        if not p.exists():
            raise FileNotFoundError(f"Provider config not found: {p}")

        raw = p.read_text(encoding="utf-8").strip()
        if not raw:
            raise ValueError(f"Provider config file is empty: {p}")

        try:
            data = yaml.safe_load(raw)
        except yaml.YAMLError as exc:
            raise ValueError(f"Malformed YAML in {p}: {exc}") from exc

        if data is None:
            raise ValueError(f"Provider config file is empty (null document): {p}")
        if not isinstance(data, dict):
            raise ValueError(
                f"Expected a mapping in {p}, got {type(data).__name__}"
            )

        return cls(**data)

    def to_yaml(self, path: Path | str | None = None) -> str | None:
        """Serialize this config to YAML string.

        If *path* is provided, also write the file to disk.
        Returns the YAML string.
        """
        data = self.model_dump(exclude_none=True, exclude_defaults=True)
        yaml_str = yaml.dump(data, default_flow_style=False, sort_keys=False)
        if path is not None:
            Path(path).write_text(yaml_str, encoding="utf-8")
        return yaml_str

    def resolve_api_key(self) -> Optional[str]:
        """Return the API key, falling back to env var if not set.

        Checks ``LLM_PROXY_<PROVIDER>_API_KEY`` (uppercased name with
        hyphens replaced by underscores).
        """
        if self.api_key:
            return self.api_key
        import os

        env_key = f"LLM_PROXY_{self.name.upper().replace('-', '_')}_API_KEY"
        return os.environ.get(env_key)

    def get_model(self, model_id: str) -> Optional[ModelConfig]:
        """Look up a model by its ``id`` field.  Returns ``None`` if not found."""
        for m in self.models:
            if m.id == model_id:
                return m
        return None
