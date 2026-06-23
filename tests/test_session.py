"""Tests for ``src.utils.session`` — disk persistence for storage state."""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from src.utils.session import (
    SESSIONS_DIR,
    delete_storage_state,
    ensure_sessions_dir,
    list_sessions,
    load_storage_state,
    save_storage_state,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

SAMPLE_STATE = {
    "cookies": [
        {
            "name": "session_id",
            "value": "abc123def456",
            "domain": ".openai.com",
            "path": "/",
            "httpOnly": True,
            "secure": True,
            "sameSite": "Lax",
        },
        {
            "name": "csrf_token",
            "value": "xyz789",
            "domain": ".openai.com",
            "path": "/",
            "httpOnly": True,
            "secure": True,
            "sameSite": "Strict",
        },
    ],
    "localStorage": {
        "theme": "dark",
        "token": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0",
        "pref_lang": "pt-BR",
    },
}


@pytest.fixture
def tmp_sessions_dir(monkeypatch, tmp_path):
    """Redirect ``SESSIONS_DIR`` to a temporary path."""
    monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_path)
    return tmp_path


# ---------------------------------------------------------------------------
# Unit: ensure_sessions_dir
# ---------------------------------------------------------------------------


class TestEnsureSessionsDir:
    def test_creates_directory(self, tmp_sessions_dir):
        """Should create the session directory if it does not exist."""
        tmp_sessions_dir.rmdir()  # tmp_path already exists; remove to test creation
        assert not tmp_sessions_dir.exists()
        result = ensure_sessions_dir()
        assert result == tmp_sessions_dir
        assert tmp_sessions_dir.is_dir()

    def test_idempotent(self, tmp_sessions_dir):
        """Calling twice should not raise."""
        ensure_sessions_dir()
        ensure_sessions_dir()  # second call must pass
        assert tmp_sessions_dir.is_dir()


# ---------------------------------------------------------------------------
# Unit: save + load round-trip
# ---------------------------------------------------------------------------


class TestSaveLoadRoundTrip:
    def test_save_creates_file(self, tmp_sessions_dir):
        """``save_storage_state`` should write a JSON file."""
        path = save_storage_state("api.openai.com", SAMPLE_STATE)
        assert path.exists()
        assert path.name == "api.openai.com.json"
        assert path.parent == tmp_sessions_dir

    def test_save_creates_directory_automatically(self, monkeypatch, tmp_path):
        """Directory is auto-created if missing (no pre-existing tmp)."""
        fresh = tmp_path / "sessions"
        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", fresh)
        assert not fresh.exists()
        save_storage_state("groq.com", {"cookies": [], "localStorage": {}})
        assert fresh.is_dir()

    def test_round_trip_exact(self, tmp_sessions_dir):
        """Saved state must be byte-for-byte recoverable."""
        save_storage_state("api.openai.com", SAMPLE_STATE)
        loaded = load_storage_state("api.openai.com")
        assert loaded == SAMPLE_STATE

    def test_round_trip_empty_cookies(self, tmp_sessions_dir):
        """Empty cookies and empty localStorage round-trip correctly."""
        empty = {"cookies": [], "localStorage": {}}
        save_storage_state("empty.example.com", empty)
        loaded = load_storage_state("empty.example.com")
        assert loaded == empty

    def test_round_trip_no_local_storage(self, tmp_sessions_dir):
        """State with only cookies still round-trips (localStorage defaults
        to empty dict)."""
        only_cookies = {"cookies": [{"name": "foo", "value": "bar", "domain": ".example.com"}]}
        save_storage_state("cookies-only.example.com", only_cookies)
        loaded = load_storage_state("cookies-only.example.com")
        assert loaded["cookies"] == only_cookies["cookies"]
        assert loaded["localStorage"] == {}  # defaulted

    def test_round_trip_no_cookies(self, tmp_sessions_dir):
        """State with only localStorage still round-trips (cookies default
        to empty list)."""
        only_ls = {"localStorage": {"key": "val"}}
        save_storage_state("ls-only.example.com", only_ls)
        loaded = load_storage_state("ls-only.example.com")
        assert loaded["localStorage"] == {"key": "val"}
        assert loaded["cookies"] == []  # defaulted

    def test_host_namespace_isolation(self, tmp_sessions_dir):
        """Different hosts must not interfere with each other."""
        save_storage_state("host-a.com", {"cookies": [], "localStorage": {"a": "1"}})
        save_storage_state("host-b.com", {"cookies": [], "localStorage": {"b": "2"}})
        assert load_storage_state("host-a.com")["localStorage"] == {"a": "1"}
        assert load_storage_state("host-b.com")["localStorage"] == {"b": "2"}

    def test_extra_keys_preserved(self, tmp_sessions_dir):
        """Unknown keys in the state dict must be preserved verbatim."""
        extended = {
            "cookies": [],
            "localStorage": {},
            "captchaToken": "abc",
            "sessionTimestamp": 1700000000,
        }
        save_storage_state("extended.example.com", extended)
        loaded = load_storage_state("extended.example.com")
        assert loaded["captchaToken"] == "abc"
        assert loaded["sessionTimestamp"] == 1700000000


# ---------------------------------------------------------------------------
# Unit: load — edge cases
# ---------------------------------------------------------------------------


class TestLoadEdgeCases:
    def test_load_missing_file_returns_empty(self, tmp_sessions_dir):
        """Missing file should return empty state, not raise."""
        state = load_storage_state("nonexistent.host")
        assert state == {"cookies": [], "localStorage": {}}

    def test_load_empty_file_returns_empty(self, tmp_sessions_dir):
        """Empty file should return empty state, not raise."""
        (tmp_sessions_dir / "empty.host.json").write_text("", encoding="utf-8")
        state = load_storage_state("empty.host")
        assert state == {"cookies": [], "localStorage": {}}

    def test_load_malformed_json_returns_empty(self, tmp_sessions_dir):
        """Corrupt JSON should return empty state, not raise."""
        (tmp_sessions_dir / "broken.host.json").write_text(
            "{invalid: [broken", encoding="utf-8"
        )
        state = load_storage_state("broken.host")
        assert state == {"cookies": [], "localStorage": {}}

    def test_load_non_dict_json_returns_empty(self, tmp_sessions_dir):
        """JSON that is not a dict should return empty state."""
        (tmp_sessions_dir / "list.host.json").write_text(
            json.dumps(["not", "a", "dict"]), encoding="utf-8"
        )
        state = load_storage_state("list.host")
        assert state == {"cookies": [], "localStorage": {}}


# ---------------------------------------------------------------------------
# Unit: delete
# ---------------------------------------------------------------------------


class TestDelete:
    def test_delete_existing_returns_true(self, tmp_sessions_dir):
        save_storage_state("delete-me.com", {"cookies": [], "localStorage": {}})
        assert delete_storage_state("delete-me.com") is True
        assert not (tmp_sessions_dir / "delete-me.com.json").exists()

    def test_delete_missing_returns_false(self, tmp_sessions_dir):
        assert delete_storage_state("never-existed.com") is False

    def test_after_delete_load_returns_empty(self, tmp_sessions_dir):
        save_storage_state("transient.com", {"cookies": [], "localStorage": {"k": "v"}})
        delete_storage_state("transient.com")
        assert load_storage_state("transient.com") == {"cookies": [], "localStorage": {}}


# ---------------------------------------------------------------------------
# Unit: list_sessions
# ---------------------------------------------------------------------------


class TestListSessions:
    def test_empty_dir_returns_empty_list(self, tmp_sessions_dir):
        assert list_sessions() == []

    def test_lists_hosts_alphabetically(self, tmp_sessions_dir):
        save_storage_state("z.com", {"cookies": [], "localStorage": {}})
        save_storage_state("a.com", {"cookies": [], "localStorage": {}})
        save_storage_state("m.com", {"cookies": [], "localStorage": {}})
        assert list_sessions() == ["a.com", "m.com", "z.com"]

    def test_ignores_non_json_files(self, tmp_sessions_dir):
        (tmp_sessions_dir / "readme.txt").write_text("hi", encoding="utf-8")
        (tmp_sessions_dir / "data.csv").write_text("a,b", encoding="utf-8")
        assert list_sessions() == []

    def test_non_existent_dir_returns_empty(self, tmp_sessions_dir):
        """When the sessions dir does not exist, return empty list."""
        tmp_sessions_dir.rmdir()
        assert list_sessions() == []


# ---------------------------------------------------------------------------
# Unit: session expiration
# ---------------------------------------------------------------------------


class TestIsSessionExpired:
    """Tests for ``is_session_expired()``."""

    def test_no_session_file_returns_true(self, tmp_sessions_dir):
        """Missing session file should be reported as expired."""
        from src.utils.session import is_session_expired

        assert is_session_expired("never.logged.in") is True

    def test_no_cookies_returns_true(self, tmp_sessions_dir):
        """State with zero cookies is expired — no auth material."""
        from src.utils.session import is_session_expired, save_storage_state

        save_storage_state("no-cookies.host", {"cookies": [], "localStorage": {}})
        assert is_session_expired("no-cookies.host") is True

    def test_session_cookie_no_expiry_is_valid(self, tmp_sessions_dir):
        """A cookie without an ``expires`` field is a session cookie → valid."""
        from src.utils.session import is_session_expired, save_storage_state

        save_storage_state(
            "session-only.host",
            {
                "cookies": [
                    {
                        "name": "session_id",
                        "value": "abc",
                        "domain": ".example.com",
                        "path": "/",
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("session-only.host") is False

    def test_cookie_with_expires_minus_one_is_session_cookie(self, tmp_sessions_dir):
        """``expires=-1`` indicates a session cookie (Playwright convention)."""
        from src.utils.session import is_session_expired, save_storage_state

        save_storage_state(
            "expires-minus1.host",
            {
                "cookies": [
                    {
                        "name": "session_id",
                        "value": "abc",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": -1,
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("expires-minus1.host") is False

    def test_all_cookies_expired_returns_true(self, tmp_sessions_dir):
        """When every cookie's expires is in the past → expired."""
        from src.utils.session import is_session_expired, save_storage_state

        past = 1000000  # way in the past
        save_storage_state(
            "all-expired.host",
            {
                "cookies": [
                    {
                        "name": "old1",
                        "value": "a",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": past,
                    },
                    {
                        "name": "old2",
                        "value": "b",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": past,
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("all-expired.host") is True

    def test_future_expiry_is_valid(self, tmp_sessions_dir):
        """A single cookie with future expires → session is valid."""
        from src.utils.session import is_session_expired, save_storage_state

        future = 9999999999  # far in the future
        save_storage_state(
            "future.host",
            {
                "cookies": [
                    {
                        "name": "valid",
                        "value": "c",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": future,
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("future.host") is False

    def test_mixed_expiry_one_valid_keeps_session_alive(self, tmp_sessions_dir):
        """At least one valid cookie (future or session) → not expired."""
        from src.utils.session import is_session_expired, save_storage_state

        past = 1000000
        future = 9999999999
        save_storage_state(
            "mixed.host",
            {
                "cookies": [
                    {
                        "name": "expired",
                        "value": "d",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": past,
                    },
                    {
                        "name": "still_good",
                        "value": "e",
                        "domain": ".example.com",
                        "path": "/",
                        "expires": future,
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("mixed.host") is False

    def test_max_age_exceeded_returns_true(self, tmp_sessions_dir):
        """Session file older than ``max_age_seconds`` → expired."""
        from src.utils.session import is_session_expired, save_storage_state

        save_storage_state(
            "old-file.host",
            {
                "cookies": [
                    {
                        "name": "session",
                        "value": "x",
                        "domain": ".example.com",
                        "path": "/",
                    },
                ],
                "localStorage": {},
            },
        )
        # Simulate an old file by setting mtime far in the past
        import time

        old_mtime = time.time() - 7200  # 2 hours ago
        path = tmp_sessions_dir / "old-file.host.json"
        os.utime(path, (old_mtime, old_mtime))

        assert is_session_expired("old-file.host", max_age_seconds=3600) is True

    def test_within_max_age_returns_false(self, tmp_sessions_dir):
        """Session file newer than ``max_age_seconds`` → not expired."""
        from src.utils.session import is_session_expired, save_storage_state

        save_storage_state(
            "fresh.host",
            {
                "cookies": [
                    {
                        "name": "session",
                        "value": "y",
                        "domain": ".example.com",
                        "path": "/",
                    },
                ],
                "localStorage": {},
            },
        )
        assert is_session_expired("fresh.host", max_age_seconds=3600) is False
# ---------------------------------------------------------------------------


class TestFileContent:
    def test_json_is_pretty_printed(self, tmp_sessions_dir):
        """Saved JSON should be human-readable with indentation."""
        path = save_storage_state("pretty-test.com", {"cookies": [], "localStorage": {}})
        raw = path.read_text(encoding="utf-8")
        assert "\n" in raw
        parsed = json.loads(raw)
        assert parsed == {"cookies": [], "localStorage": {}}

    def test_filename_matches_host(self, tmp_sessions_dir):
        """File should be named ``<host>.json``."""
        save_storage_state("chat.openai.com", SAMPLE_STATE)
        assert (tmp_sessions_dir / "chat.openai.com.json").exists()
        assert not (tmp_sessions_dir / "chat_openai_com.json").exists()


# ---------------------------------------------------------------------------
# Unit: session refresh
# ---------------------------------------------------------------------------


class TestRefreshSession:
    """Tests for ``refresh_session()``."""

    def test_valid_session_returns_without_browser(self, tmp_sessions_dir, monkeypatch):
        """When session is valid, return loaded state — no browser launched."""
        from src.utils.session import refresh_session, save_storage_state, SESSIONS_DIR

        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_sessions_dir)

        save_storage_state(
            "valid.host",
            {
                "cookies": [
                    {
                        "name": "good",
                        "value": "abc",
                        "domain": ".example.com",
                        "expires": 9999999999,
                    },
                ],
                "localStorage": {"k": "v"},
            },
        )

        # If session is valid, no capture should be triggered
        import builtins

        original_input = builtins.input

        def fail_if_called(*args, **kwargs):
            raise AssertionError("browser login should not be triggered for valid session")

        monkeypatch.setattr(builtins, "input", fail_if_called)

        state = refresh_session("valid.host")
        assert state["cookies"][0]["name"] == "good"
        assert state["localStorage"] == {"k": "v"}

    def test_expired_session_launches_browser_and_saves(
        self, tmp_sessions_dir, monkeypatch
    ):
        """Expired session triggers browser capture and saves fresh state."""
        from src.utils.session import refresh_session, save_storage_state, SESSIONS_DIR

        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_sessions_dir)

        # Save an expired session
        save_storage_state(
            "expired.host",
            {
                "cookies": [
                    {
                        "name": "old",
                        "value": "stale",
                        "domain": ".example.com",
                        "expires": 1000000,  # past
                    },
                ],
                "localStorage": {},
            },
        )

        # Mock the browser capture to return fresh state
        fresh_state = {
            "cookies": [
                {
                    "name": "fresh_session",
                    "value": "new123",
                    "domain": ".example.com",
                },
            ],
            "localStorage": {"token": "eyJnew"},
            "origin": "https://expired.host",
        }

        monkeypatch.setattr(
            "src.cli.browser_login.capture_storage_state_interactive",
            lambda host_or_url: fresh_state,
        )

        # Also mock input() since capture_storage_state_interactive calls it
        monkeypatch.setattr("builtins.input", lambda prompt="": "")

        state = refresh_session("expired.host", silent=True)

        # Should return the fresh state
        assert state["cookies"][0]["name"] == "fresh_session"
        assert state["localStorage"]["token"] == "eyJnew"

        # Should have persisted the fresh state to disk
        loaded = load_storage_state("expired.host")
        assert loaded["cookies"][0]["name"] == "fresh_session"
        assert loaded["localStorage"]["token"] == "eyJnew"

    def test_empty_host_raises_value_error(self, tmp_sessions_dir, monkeypatch):
        """Empty or whitespace-only host should raise ValueError."""
        from src.utils.session import refresh_session

        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_sessions_dir)

        with pytest.raises(ValueError, match="non-empty"):
            refresh_session("")

        with pytest.raises(ValueError, match="non-empty"):
            refresh_session("   ")

    def test_browser_capture_failure_propagates(
        self, tmp_sessions_dir, monkeypatch
    ):
        """If capture fails, RuntimeError propagates."""
        from src.utils.session import refresh_session, save_storage_state

        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_sessions_dir)

        save_storage_state(
            "broken.host",
            {
                "cookies": [
                    {
                        "name": "old",
                        "value": "x",
                        "domain": ".example.com",
                        "expires": 1000000,
                    },
                ],
                "localStorage": {},
            },
        )

        # Browser capture raises RuntimeError
        def failing_capture(host_or_url):
            raise RuntimeError("No display available")

        monkeypatch.setattr(
            "src.cli.browser_login.capture_storage_state_interactive",
            failing_capture,
        )
        monkeypatch.setattr("builtins.input", lambda prompt="": "")

        with pytest.raises(RuntimeError, match="No display available"):
            refresh_session("broken.host", silent=True)

    def test_unknown_exception_wrapped_in_runtime_error(
        self, tmp_sessions_dir, monkeypatch
    ):
        """Non-RuntimeError exceptions are wrapped in RuntimeError."""
        from src.utils.session import refresh_session, save_storage_state

        monkeypatch.setattr("src.utils.session.SESSIONS_DIR", tmp_sessions_dir)

        save_storage_state(
            "bad.host",
            {
                "cookies": [
                    {
                        "name": "old",
                        "value": "x",
                        "domain": ".example.com",
                        "expires": 1000000,
                    },
                ],
                "localStorage": {},
            },
        )

        def weird_capture(host_or_url):
            raise PermissionError("cannot access browser")

        monkeypatch.setattr(
            "src.cli.browser_login.capture_storage_state_interactive",
            weird_capture,
        )
        monkeypatch.setattr("builtins.input", lambda prompt="": "")

        with pytest.raises(RuntimeError, match="Failed to capture browser state"):
            refresh_session("bad.host", silent=True)
