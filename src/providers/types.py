"""Pydantic models for provider request/response types.

Defines the domain types used by the AIProvider interface for
chat completion requests and responses, modelled after the
OpenAI chat completions API schema.

Usage::

    from src.providers.types import ChatRequest, ChatResponse

    req = ChatRequest(
        model="gpt-4o",
        messages=[{"role": "user", "content": "Hello"}],
    )
    # req.model == "gpt-4o"
    # req.messages[0].content == "Hello"
"""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


# ---------------------------------------------------------------------------
# ChatMessage — a single turn in a conversation
# ---------------------------------------------------------------------------


class ChatMessage(BaseModel):
    """A single message in a chat conversation.

    Attributes
    ----------
    role :
        Who sent the message (``"system"``, ``"user"``, ``"assistant"``).
    content :
        The message body text.
    """

    role: str = Field(..., description="Message author: system, user, or assistant")
    content: str = Field(..., description="Message body text")

    model_config = {"extra": "forbid"}

    def __str__(self) -> str:
        return f"[{self.role}] {self.content[:80]}..."


# ---------------------------------------------------------------------------
# ChatRequest — the input to a provider's chat completion endpoint
# ---------------------------------------------------------------------------


class ChatRequest(BaseModel):
    """A chat completion request sent to an LLM provider.

    Attributes
    ----------
    model :
        Model identifier (e.g. ``"gpt-4o"``, ``"claude-sonnet-4"``).
    messages :
        Ordered list of conversation messages.
    stream :
        Whether to stream the response token by token.
    max_tokens :
        Maximum number of tokens to generate.
    temperature :
        Sampling temperature (0.0 = deterministic, 2.0 = creative).
    """

    model: str = Field(..., description="Model identifier")
    messages: list[ChatMessage] = Field(
        ..., description="Ordered conversation messages"
    )
    stream: bool = Field(default=False, description="Enable streaming response")
    max_tokens: Optional[int] = Field(
        default=None, ge=1, description="Max tokens to generate"
    )
    temperature: Optional[float] = Field(
        default=None, ge=0.0, le=2.0, description="Sampling temperature"
    )

    model_config = {"extra": "forbid"}

    @classmethod
    def from_dict(cls, data: dict) -> ChatRequest:
        """Build a ``ChatRequest`` from a raw dict (e.g. from a JSON POST body).

        Converts the ``messages`` list entries to ``ChatMessage`` instances
        automatically if they are plain dicts.
        """
        messages_raw = data.get("messages", [])
        messages = [
            m if isinstance(m, ChatMessage) else ChatMessage(**m)
            for m in messages_raw
        ]
        return cls(messages=messages, **{k: v for k, v in data.items() if k != "messages"})


# ---------------------------------------------------------------------------
# ChatResponseChoice — one completion option
# ---------------------------------------------------------------------------


class ChatResponseChoice(BaseModel):
    """A single completion choice returned by the provider.

    Attributes
    ----------
    index :
        Choice index (0-based).
    message :
        The assistant's response message.
    finish_reason :
        Why the provider stopped generating (``"stop"``, ``"length"``,
        ``"content_filter"``, etc.).
    """

    index: int = Field(default=0, ge=0, description="Choice index (0-based)")
    message: ChatMessage = Field(..., description="Assistant response message")
    finish_reason: str = Field(
        default="stop", description="Reason generation stopped"
    )

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# Usage — token consumption
# ---------------------------------------------------------------------------


class Usage(BaseModel):
    """Token usage statistics for a chat completion.

    Attributes
    ----------
    prompt_tokens :
        Number of tokens in the prompt.
    completion_tokens :
        Number of tokens generated.
    total_tokens :
        Sum of prompt + completion tokens.
    """

    prompt_tokens: int = Field(default=0, ge=0, description="Prompt token count")
    completion_tokens: int = Field(
        default=0, ge=0, description="Generated token count"
    )
    total_tokens: int = Field(default=0, ge=0, description="Total token count")

    model_config = {"extra": "forbid"}


# ---------------------------------------------------------------------------
# ChatResponse — the output from a provider's chat completion endpoint
# ---------------------------------------------------------------------------


class ChatResponse(BaseModel):
    """A chat completion response from an LLM provider.

    Attributes
    ----------
    id :
        Unique response identifier.
    object :
        Object type (typically ``"chat.completion"``).
    created :
        Unix timestamp of when the response was generated.
    model :
        Model that generated the response.
    choices :
        List of completion choices (usually 1 for non-streaming).
    usage :
        Token usage statistics.
    """

    id: str = Field(..., description="Response identifier")
    object: str = Field(
        default="chat.completion", description="Object type identifier"
    )
    created: int = Field(..., description="Unix timestamp of generation time")
    model: str = Field(..., description="Model that generated the response")
    choices: list[ChatResponseChoice] = Field(
        ..., description="Completion choices (usually 1)"
    )
    usage: Optional[Usage] = Field(default=None, description="Token usage stats")

    model_config = {"extra": "forbid"}

    def primary_content(self) -> str:
        """Shortcut: return the content of the first choice's message.

        Raises ``IndexError`` if there are no choices.
        """
        return self.choices[0].message.content
