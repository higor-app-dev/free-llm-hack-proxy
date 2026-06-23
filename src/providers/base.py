"""Abstract base class for all LLM providers.

Every provider implementation (OpenAI, Anthropic, Groq, HuggingFace, etc.)
must subclass ``AIProvider`` and implement the five methods defined here.

The interface is designed so the proxy can treat all providers uniformly:

* ``Name()`` → identifier for routing and config lookups
* ``Models()`` → what models this provider offers
* ``Login()`` → authentication setup
* ``IsSessionValid()`` → liveness check
* ``Prompt()`` → the actual chat completion call

Usage::

    from src.providers.base import AIProvider
    from src.providers.types import ChatRequest

    class MyProvider(AIProvider):
        ...

        async def Prompt(self, request: ChatRequest) -> ChatResponse:
            # implementation here
            ...
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Optional

from src.providers.types import ChatRequest, ChatResponse


class AIProvider(ABC):
    """Abstract interface for an LLM provider.

    Subclasses must implement all five abstract methods.  The interface
    is deliberately kept small — configuration (API keys, base URLs,
    rate limits) is expected to come from the :class:`ProviderConfig`
    loaded by :mod:`src.providers.config` rather than constructor args.

    Thread-safety is **not** guaranteed by this base class; individual
    providers should document their own safety guarantees.
    """

    # ------------------------------------------------------------------
    # Identity
    # ------------------------------------------------------------------

    @abstractmethod
    def Name(self) -> str:
        """Return the unique provider identifier (slug).

        Returns
        -------
        str
            Lowercase provider name (e.g. ``"openai"``, ``"anthropic"``,
            ``"groq"``).  Must match the name used in provider YAML
            configs so the router can map ``load_provider_config(name)``
            to the correct implementation class.
        """
        ...

    # ------------------------------------------------------------------
    # Discovery
    # ------------------------------------------------------------------

    @abstractmethod
    def Models(self) -> list[str]:
        """Return the list of model identifiers this provider supports.

        Returns
        -------
        list[str]
            Model IDs (e.g. ``["gpt-4o", "gpt-4o-mini"]``).  These
            correspond to the ``id`` field in :class:`ModelConfig`
            entries in the provider's YAML file.

        The list may be empty if the provider has not been configured
        with any models yet.
        """
        ...

    # ------------------------------------------------------------------
    # Authentication
    # ------------------------------------------------------------------

    @abstractmethod
    def Login(self, api_key: Optional[str] = None, **kwargs: str) -> bool:
        """Authenticate with the provider and prepare a session.

        Parameters
        ----------
        api_key :
            Optional API key.  When ``None``, the implementation should
            fall back to the provider's configured key (from YAML or
            environment variable).
        **kwargs :
            Provider-specific authentication parameters (e.g. an
            organisation ID, a custom endpoint, or a browser-session
            token for cookie-based auth).

        Returns
        -------
        bool
            ``True`` if authentication succeeded and the provider is
            ready to serve requests, ``False`` if the key is invalid
            or the session could not be established.

        Implementations should raise :class:`ValueError` when given
        obviously invalid parameters (empty key, malformed token, etc.).
        """
        ...

    # ------------------------------------------------------------------
    # Session liveness
    # ------------------------------------------------------------------

    @abstractmethod
    def IsSessionValid(self) -> bool:
        """Check whether the current session is still usable.

        Returns
        -------
        bool
            ``True`` if the provider is authenticated and ready,
            ``False`` if the session has expired, been revoked, or
            was never established.

        This is a **fast check** that should not make network calls
        (e.g. it checks a cached token expiry or a stored cookie
        timestamp).  Expensive validation belongs in :meth:`Login`.
        """
        ...

    # ------------------------------------------------------------------
    # Chat completion
    # ------------------------------------------------------------------

    @abstractmethod
    async def Prompt(self, request: ChatRequest) -> ChatResponse:
        """Send a chat completion request and return the response.

        Parameters
        ----------
        request :
            The chat request containing model selection, messages,
            and optional generation parameters.

        Returns
        -------
        ChatResponse
            The provider's response, normalised into the common
            ``ChatResponse`` shape (OpenAI-compatible structure).

        Raises
        ------
        RuntimeError
            If the request could not be sent or the provider returned
            an unexpected error (network failure, malformed response,
            etc.).  HTTP-level errors (4xx, 5xx) should be translated
            into typed exceptions.

        **Implementations must:**

        1. Map the internal ``ChatRequest`` to the provider's native
           request format.
        2. Make the HTTP call (or invoke the SDK).
        3. Map the provider's response back to ``ChatResponse``.
        4. Handle provider-specific error shapes without crashing.

        **Streaming** is **not** handled by this method — it always
        returns a complete ``ChatResponse``.  Streaming support may
        be added as a separate method in a future extension.
        """
        ...
