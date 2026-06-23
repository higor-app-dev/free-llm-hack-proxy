#!/usr/bin/env python3
"""Disk persistence for browser storage state (cookies + localStorage).

Provides save/load round-tripping of ``StorageState`` to JSON files
under ``~/.llm-proxy/sessions/``, namespaced per host.

Typical usage::

    from src.utils.session import save_storage_state, load_storage_state

    # Save the current browser state for a host
    state = {
        "cookies": [
            {
                "name": "session_id",
                "value": "abc123",
                "domain": ".openai.com",
                "path": "/",
                "httpOnly": True,
                "secure": True,
                "sameSite": "Lax",
            },
        ],
        "localStorage": {
            "theme": "dark",
            "token": "eyJ...",
        },
    }
    save_storage_state("api.openai.com", state)

    # Load it back later
    restored = load_storage_state("api.openai.com")
"""

from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Any, Optional

# Re-export key types so callers don't need separate imports
__all__ = [
    "SESSIONS_DIR",
    "ensure_sessions_dir",
    "save_storage_state",
    "load_storage_state",
    "delete_storage_state",
    "is_session_expired",
    "list_sessions",
    "refresh_session",
]

logger = logging.getLogger("utils.session")

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

SESSIONS_DIR = Path.home() / ".llm-proxy" / "sessions"


def ensure_sessions_dir() -> Path:
    """Create ``~/.llm-proxy/sessions/`` if it does not exist.

    Returns the sessions directory path (idempotent — safe to call multiple
    times).
    """
    SESSIONS_DIR.mkdir(parents=True, exist_ok=True)
    return SESSIONS_DIR


# ---------------------------------------------------------------------------
# Storage state helpers
# ---------------------------------------------------------------------------

def _session_path(host: str) -> Path:
    """Return the JSON file path for a given *host*.

    The host is used directly as the filename stem with ``.json`` appended
    (e.g. ``api.openai.com.json``).  No sanitisation beyond stripping is
    applied — hostnames are assumed to be well-formed.
    """
    return SESSIONS_DIR / f"{host.strip()}.json"


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

def save_storage_state(host: str, state: dict[str, Any], /) -> Path:
    """Persist *state* (cookies + localStorage) to disk for *host*.

    Parameters
    ----------
    host :
        Host name used as the filename stem (e.g. ``"api.openai.com"``).
    state :
        Storage state dict with optional keys:
            - ``cookies`` (list[dict]): Browser cookies.
            - ``localStorage`` (dict[str, str]): Key-value pairs.

        Unknown keys are serialised as-is, so you may extend the shape
        freely.

    Returns
    -------
    Path
        Absolute path to the written JSON file.

    Raises
    ------
    OSError
        If the file cannot be written (permissions, disk full, etc.).
    TypeError
        If *state* contains non-serialisable values.
    """
    ensure_sessions_dir()
    path = _session_path(host)

    path.write_text(
        json.dumps(state, indent=2, ensure_ascii=False, sort_keys=True),
        encoding="utf-8",
    )

    logger.debug("Saved storage state for %s → %s (%d bytes)", host, path, path.stat().st_size)
    return path


def load_storage_state(host: str, /) -> dict[str, Any]:
    """Read the previously persisted storage state for *host*.

    Parameters
    ----------
    host :
        Host name matching a previous ``save_storage_state`` call.

    Returns
    -------
    dict
        The stored state dict (always contains at least ``"cookies"`` and
        ``"localStorage"`` keys; values default to empty list / dict when
        the file is missing or malformed).

    Examples
    --------
    >>> state = load_storage_state("api.openai.com")
    >>> state.get("cookies", [])
    [{"name": "session_id", ...}]
    >>> state.get("localStorage", {})
    {"theme": "dark"}
    """
    path = _session_path(host)

    if not path.exists():
        logger.debug("No saved state for %s (file not found: %s)", host, path)
        return {"cookies": [], "localStorage": {}}

    try:
        raw = path.read_text(encoding="utf-8").strip()
        if not raw:
            return {"cookies": [], "localStorage": {}}

        data = json.loads(raw)
        if not isinstance(data, dict):
            logger.warning(
                "Session file %s contains %s, expected dict — returning empty state",
                path,
                type(data).__name__,
            )
            return {"cookies": [], "localStorage": {}}

        # Ensure known keys exist even if the file was written by an older
        # version that omitted them.
        data.setdefault("cookies", [])
        data.setdefault("localStorage", {})

        logger.debug("Loaded storage state for %s ← %s", host, path)
        return data

    except (json.JSONDecodeError, OSError) as exc:
        logger.warning(
            "Failed to load session file %s: %s — returning empty state",
            path,
            exc,
        )
        return {"cookies": [], "localStorage": {}}


def delete_storage_state(host: str, /) -> bool:
    """Remove the persisted storage state for *host*.

    Parameters
    ----------
    host :
        Host whose session file should be deleted.

    Returns
    -------
    bool
        ``True`` if a file was found and removed, ``False`` if no session
        existed for *host*.
    """
    path = _session_path(host)
    if not path.exists():
        logger.debug("No session file to delete for %s", host)
        return False

    path.unlink()
    logger.debug("Deleted session file for %s (%s)", host, path)
    return True


def is_session_expired(
    host: str, /, *, max_age_seconds: Optional[float] = None
) -> bool:
    """Check whether a saved session for *host* is expired.

    A session is considered **expired** when:

    1. No session file exists for *host* → ``True`` (nothing to use).
    2. The stored state has zero cookies → ``True`` (no auth material).
    3. **Every** cookie that carries an ``expires`` field (a Unix timestamp
       in seconds) has a value in the past **and** there are zero session
       cookies (cookies without a positive ``expires``) — i.e. no cookie
       can still be valid.

    A cookie with **no** ``expires`` field, or one set to ``-1`` / ``0``,
    is treated as a **session cookie** — it has no fixed expiry and is
    considered valid for the purpose of this check.

    Parameters
    ----------
    host :
        Host name whose persisted session should be checked.
    max_age_seconds :
        Optional wall-clock age limit.  When set, the session file's
        *mtime* is compared against ``time.time()`` — if the file is
        older than *max_age_seconds* the session is considered expired
        regardless of individual cookie expiries.

    Returns
    -------
    bool
        ``True`` if the session is expired or unavailable, ``False``
        if at least one cookie is still valid and the file is within
        the optional age limit.

    Examples
    --------
    >>> is_session_expired("api.openai.com")
    False

    >>> is_session_expired("never-logged-in.com")
    True

    >>> is_session_expired("old-session.com", max_age_seconds=3600)
    True
    """
    import time as _time

    state = load_storage_state(host)

    # --- No cookies at all → expired ---
    cookies: list[dict] = state.get("cookies", [])
    if not cookies:
        return True

    # --- Optional wall-clock age check ---
    if max_age_seconds is not None and max_age_seconds > 0:
        path = _session_path(host)
        if path.exists():
            age = _time.time() - path.stat().st_mtime
            if age > max_age_seconds:
                return True

    # --- Check individual cookies ---
    now = _time.time()
    has_valid_cookie = False

    for c in cookies:
        expires = c.get("expires")
        # Session cookie (no expiry / -1 / 0) — always valid
        if expires is None or (isinstance(expires, (int, float)) and expires <= 0):
            has_valid_cookie = True
            continue
        # Persistent cookie with a future expiry — valid
        if isinstance(expires, (int, float)) and expires > now:
            has_valid_cookie = True
            continue

    return not has_valid_cookie


# ---------------------------------------------------------------------------
# Session refresh
# ---------------------------------------------------------------------------


def refresh_session(
    host: str, /, *, silent: bool = False, max_age_seconds: Optional[float] = None
) -> dict[str, Any]:
    """Ensure a valid session exists for *host*, refreshing via browser if expired.

    This is the **silent refresh** entry-point: it checks whether the stored
    session for *host* is still valid and, if not, launches the interactive
    browser-based login flow so the user can re-authenticate without the
    proxy returning hard errors.

    Parameters
    ----------
    host :
        Host name used as the session file key (e.g. ``\"chatgpt.com\"``).
    silent :
        If ``True``, suppresses stdout messages (relies on logging only).
        Default ``False`` so the user sees a clear description of what is
        happening.
    max_age_seconds :
        Optional wall-clock age limit forwarded to ``is_session_expired``.

    Returns
    -------
    dict
        The (refreshed or still-valid) storage state with keys:
        ``cookies`` (list[dict]), ``localStorage`` (dict[str, str]),
        and any extra keys that were saved.

    Raises
    ------
    RuntimeError
        If the browser cannot be launched or the capture fails.

    Examples
    --------
    >>> state = refresh_session(\"chatgpt.com\")
    >>> state[\"cookies\"]
    [...]

    >>> state = refresh_session(\"chatgpt.com\", silent=True)
    """
    _host = host.strip()
    if not _host:
        raise ValueError("host must be a non-empty string")

    expired = is_session_expired(_host, max_age_seconds=max_age_seconds)

    if not expired:
        logger.debug("Session for %s is still valid — no refresh needed", _host)
        return load_storage_state(_host)

    # --- Session expired — launch browser for re-login ---
    logger.info("Session for %s is expired — launching browser login", _host)

    if not silent:
        import sys as _sys

        print(
            f"\n  [yellow]Session for {_host} has expired or is missing.[/]",
            file=_sys.stderr,
        )
        print(
            "  [yellow]Opening browser so you can log in again...[/]",
            file=_sys.stderr,
        )

    from src.cli.browser_login import capture_storage_state_interactive

    try:
        state = capture_storage_state_interactive(_host)
    except RuntimeError:
        raise
    except Exception as exc:
        raise RuntimeError(
            f"Failed to capture browser state for {_host}: {exc}"
        ) from exc

    # Build the persist-able slice
    persist = {
        "cookies": state.get("cookies", []),
        "localStorage": state.get("localStorage", {}),
    }

    # Determine the session key from the captured origin
    from urllib.parse import urlparse

    origin = state.get("origin", "")
    parsed = urlparse(origin)
    session_key = parsed.hostname or _host

    saved_path = save_storage_state(session_key, persist)

    cookie_count = len(persist["cookies"])
    ls_count = len(persist["localStorage"])

    logger.info(
        "Refreshed session for %s → %s (%d cookies, %d localStorage keys)",
        session_key,
        saved_path,
        cookie_count,
        ls_count,
    )

    if not silent:
        import sys as _sys

        print(
            f"\n  [green]✓ Session refreshed for [bold]{session_key}[/][/]",
            file=_sys.stderr,
        )
        print(
            f"    Cookies: {cookie_count}  |  Storage keys: {ls_count}",
            file=_sys.stderr,
        )

    return persist


def list_sessions() -> list[str]:
    """Return all host names that have persisted storage state.

    Returns
    -------
    list[str]
        Sorted list of host names (e.g. ``["api.openai.com",
        "groq.com"]``).
    """
    if not SESSIONS_DIR.exists():
        return []

    hosts: list[str] = []
    for entry in SESSIONS_DIR.iterdir():
        if entry.is_file() and entry.suffix == ".json":
            host = entry.stem
            hosts.append(host)

    return sorted(hosts)
