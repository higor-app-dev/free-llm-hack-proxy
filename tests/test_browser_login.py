"""Tests for ``src.cli.browser_login`` — interactive browser login.

The core capture function ``capture_storage_state_interactive`` launches
a real headful browser, so we mock ``playwright.sync_api`` for unit tests.
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from src.cli.main import app

runner = CliRunner()

# ---------------------------------------------------------------------------
# Fixtures — mock the Playwright session
# ---------------------------------------------------------------------------


@pytest.fixture
def mock_playwright():
    """Return a properly wired mock of the Playwright sync API.

    Mocks the ``with sync_playwright() as pw:`` pattern correctly:

        sync_playwright() → context_manager
        context_manager.__enter__() → pw_instance
        pw_instance.chromium.launch() → browser
        browser.new_context() → context
        context.new_page() → page
    """
    page = MagicMock()
    page.evaluate.side_effect = lambda expr: {
        "() => JSON.parse(JSON.stringify(window.localStorage))": {
            "token": "eyJ",
            "theme": "dark",
        },
        "() => document.location.origin": "https://chatgpt.com",
    }.get(expr, "")

    context = MagicMock()
    context.storage_state.return_value = {
        "cookies": [
            {
                "name": "session",
                "value": "abc123",
                "domain": ".chatgpt.com",
                "path": "/",
                "httpOnly": True,
                "secure": True,
                "sameSite": "Lax",
            },
        ],
        "origins": [],
    }
    context.new_page.return_value = page

    browser = MagicMock()
    browser.new_context.return_value = context

    pw_instance = MagicMock()  # the object returned by __enter__
    pw_instance.chromium.launch.return_value = browser

    pw_cm = MagicMock()  # the context manager returned by sync_playwright()
    pw_cm.__enter__.return_value = pw_instance

    return {
        "pw_cm": pw_cm,
        "pw_instance": pw_instance,
        "page": page,
        "context": context,
        "browser": browser,
    }


# ---------------------------------------------------------------------------
# Unit: capture_storage_state_interactive
# ---------------------------------------------------------------------------


class TestCaptureStorageStateInteractive:
    def test_captures_cookies_and_local_storage(self, mock_playwright):
        """Captured state includes cookies, localStorage, and origin."""
        with patch(
            "playwright.sync_api.sync_playwright",
            return_value=mock_playwright["pw_cm"],
            create=True,
        ):
            with patch("builtins.input", return_value=""):
                from src.cli.browser_login import capture_storage_state_interactive

                result = capture_storage_state_interactive("chatgpt.com")

        assert "cookies" in result
        assert "localStorage" in result
        assert "origin" in result

        assert result["origin"] == "https://chatgpt.com"
        assert len(result["cookies"]) == 1
        assert result["cookies"][0]["name"] == "session"
        assert result["localStorage"] == {"token": "eyJ", "theme": "dark"}

        # Verify the browser was launched and navigated correctly
        mock_playwright["browser"].new_context.assert_called_once()
        mock_playwright["context"].new_page.assert_called_once()
        mock_playwright["page"].goto.assert_called_once_with(
            "https://chatgpt.com", wait_until="domcontentloaded"
        )
        mock_playwright["browser"].close.assert_called_once()

    def test_navigates_to_full_url_as_is(self, mock_playwright):
        """Full URLs (with scheme) should be used verbatim."""
        with patch(
            "playwright.sync_api.sync_playwright",
            return_value=mock_playwright["pw_cm"],
            create=True,
        ):
            with patch("builtins.input", return_value=""):
                from src.cli.browser_login import capture_storage_state_interactive

                capture_storage_state_interactive(
                    "https://platform.openai.com/login"
                )

        mock_playwright["page"].goto.assert_called_once_with(
            "https://platform.openai.com/login", wait_until="domcontentloaded"
        )

    def test_http_url_preserved(self, mock_playwright):
        """http:// URLs should not be rewritten to https."""
        with patch(
            "playwright.sync_api.sync_playwright",
            return_value=mock_playwright["pw_cm"],
            create=True,
        ):
            with patch("builtins.input", return_value=""):
                from src.cli.browser_login import capture_storage_state_interactive

                capture_storage_state_interactive("http://localhost:3000/login")

        mock_playwright["page"].goto.assert_called_once_with(
            "http://localhost:3000/login", wait_until="domcontentloaded"
        )

    def test_raises_runtime_error_on_import_failure(self):
        """Missing playwright should produce a clear RuntimeError."""
        # Simulate ImportError from the lazy import inside the function
        real_import = __import__

        def mock_import(name, *args, **kwargs):
            if name == "playwright.sync_api":
                raise ImportError("no module named 'playwright'")
            return real_import(name, *args, **kwargs)

        with patch("builtins.__import__", side_effect=mock_import):
            with pytest.raises(RuntimeError, match="Playwright is not installed"):
                from src.cli.browser_login import capture_storage_state_interactive

                capture_storage_state_interactive("chatgpt.com")

    def test_raises_runtime_error_on_browser_launch_failure(self, mock_playwright):
        """Browser launch failure should produce a clear RuntimeError."""
        mock_playwright["browser"].close.return_value = None  # avoid cascade
        mock_playwright["pw_instance"].chromium.launch.side_effect = Exception(
            "no display"
        )

        with patch(
            "playwright.sync_api.sync_playwright",
            return_value=mock_playwright["pw_cm"],
            create=True,
        ):
            with patch("builtins.input", return_value=""):
                with pytest.raises(RuntimeError, match="Could not launch Chromium"):
                    from src.cli.browser_login import (
                        capture_storage_state_interactive,
                    )

                    capture_storage_state_interactive("chatgpt.com")

    def test_raises_runtime_error_on_navigation_failure(self, mock_playwright):
        """Navigation errors should produce a clear RuntimeError."""
        mock_playwright["page"].goto.side_effect = Exception("DNS failure")

        with patch(
            "playwright.sync_api.sync_playwright",
            return_value=mock_playwright["pw_cm"],
            create=True,
        ):
            with patch("builtins.input", return_value=""):
                with pytest.raises(RuntimeError, match="Could not navigate"):
                    from src.cli.browser_login import (
                        capture_storage_state_interactive,
                    )

                    capture_storage_state_interactive("chatgpt.com")


# ---------------------------------------------------------------------------
# CLI integration: llm-proxy browser-login
# ---------------------------------------------------------------------------


class TestBrowserLoginCli:
    def test_cli_help_shows_command(self):
        """The ``browser-login`` command appears in --help."""
        result = runner.invoke(app, ["browser-login", "--help"])
        assert result.exit_code == 0
        assert "browser-login" in result.output
        assert "Log in via browser" in result.output
        assert "--provider" in result.output
        assert "--save-as" in result.output

    def test_cli_missing_host_shows_error(self):
        """Running without a host argument should show error."""
        result = runner.invoke(app, ["browser-login"])
        assert result.exit_code != 0
        assert "Error" in result.output or "Missing argument" in result.output

    def test_cli_happy_path(self, tmp_path):
        """Full CLI invocation with mocked capture + save."""
        with patch(
            "src.cli.browser_login.capture_storage_state_interactive",
        ) as mock_capture:
            mock_capture.return_value = {
                "cookies": [{"name": "session", "value": "abc"}],
                "localStorage": {"token": "eyJ"},
                "origin": "https://chatgpt.com",
            }

            with patch("src.utils.session.save_storage_state") as mock_save:
                mock_save.return_value = tmp_path / "chatgpt.com.json"

                with patch("builtins.input", return_value=""):
                    result = runner.invoke(
                        app,
                        ["browser-login", "chatgpt.com"],
                    )

        assert result.exit_code == 0
        assert "Session saved" in result.output
        assert "chatgpt.com" in result.output

        # Verify capture was called with the right URL
        mock_capture.assert_called_once_with("chatgpt.com")

        # Verify save was called with the right hostname
        mock_save.assert_called_once()
        args, _ = mock_save.call_args
        assert args[0] == "chatgpt.com"  # session key = hostname

    def test_cli_with_provider_option(self, tmp_path):
        """``--provider`` flag resolves the URL from the provider config.

        Important: the CLI body imports ``load_provider_config`` lazily via
        ``from src.providers import load_provider_config``, so the patch
        target is ``src.providers.load_provider_config`` (where the name
        is defined), not ``src.cli.main.load_provider_config`` (where it's
        used).
        """
        from src.providers.models import ProviderConfig

        mock_prov = ProviderConfig(
            name="openai",
            base_url="https://api.openai.com/v1",
            api_key="sk-test",
        )

        with patch(
            "src.providers.load_provider_config", return_value=mock_prov
        ):
            with patch(
                "src.cli.browser_login.capture_storage_state_interactive",
            ) as mock_capture:
                mock_capture.return_value = {
                    "cookies": [],
                    "localStorage": {},
                    "origin": "https://api.openai.com/v1",
                }

                with patch("src.utils.session.save_storage_state") as mock_save:
                    mock_save.return_value = tmp_path / "api.openai.com.json"

                    with patch("builtins.input", return_value=""):
                        result = runner.invoke(
                            app,
                            [
                                "browser-login",
                                "chatgpt.com",
                                "--provider",
                                "openai",
                            ],
                        )

        assert result.exit_code == 0
        # Browser should navigate to the provider's base URL
        mock_capture.assert_called_once_with("https://api.openai.com/v1")

    def test_cli_with_save_as_option(self, tmp_path):
        """``--save-as`` overrides the session filename stem."""
        with patch(
            "src.cli.browser_login.capture_storage_state_interactive",
        ) as mock_capture:
            mock_capture.return_value = {
                "cookies": [],
                "localStorage": {},
                "origin": "https://accounts.google.com",
            }

            with patch("src.utils.session.save_storage_state") as mock_save:
                mock_save.return_value = tmp_path / "gmail.json"

                with patch("builtins.input", return_value=""):
                    result = runner.invoke(
                        app,
                        [
                            "browser-login",
                            "https://accounts.google.com",
                            "--save-as",
                            "gmail",
                        ],
                    )

        assert result.exit_code == 0
        mock_capture.assert_called_once_with("https://accounts.google.com")
        # Saved with the --save-as key
        args, _ = mock_save.call_args
        assert args[0] == "gmail"

    def test_cli_capture_failure_shows_error(self):
        """If capture raises RuntimeError, CLI shows the message and exits."""
        with patch(
            "src.cli.browser_login.capture_storage_state_interactive",
        ) as mock_capture:
            mock_capture.side_effect = RuntimeError("No display available")

            result = runner.invoke(
                app,
                ["browser-login", "chatgpt.com"],
            )

        assert result.exit_code == 1
        assert "No display available" in result.output

    def test_cli_provider_not_found_shows_error(self):
        """Unknown provider name shows an error without calling capture.

        The CLI body imports ``load_provider_config`` lazily via
        ``from src.providers import load_provider_config``, so the patch
        target is ``src.providers.load_provider_config`` (where the name
        is defined).
        """
        with patch(
            "src.providers.load_provider_config",
            side_effect=FileNotFoundError("no such file"),
        ):
            with patch(
                "src.cli.browser_login.capture_storage_state_interactive",
            ) as mock_capture:
                result = runner.invoke(
                    app,
                    [
                        "browser-login",
                        "chatgpt.com",
                        "--provider",
                        "nonexistent",
                    ],
                )

            assert result.exit_code == 1
            mock_capture.assert_not_called()


# ---------------------------------------------------------------------------
# CLI integration: llm-proxy refresh
# ---------------------------------------------------------------------------


class TestRefreshCli:
    """Tests for the ``refresh`` CLI command."""

    def test_cli_help_shows_command(self):
        """The ``refresh`` command appears in --help."""
        result = runner.invoke(app, ["refresh", "--help"])
        assert result.exit_code == 0
        assert "refresh" in result.output
        assert "Refresh an expired" in result.output

    def test_cli_missing_host_shows_error(self):
        """Running without a host argument should show error."""
        result = runner.invoke(app, ["refresh"])
        assert result.exit_code != 0
        assert "Error" in result.output or "Missing argument" in result.output

    def test_cli_with_valid_session(self, tmp_path):
        """Refresh with a valid session passes without error."""
        with patch(
            "src.utils.session.refresh_session",
        ) as mock_refresh:
            mock_refresh.return_value = {
                "cookies": [{"name": "good", "value": "abc"}],
                "localStorage": {},
            }
            result = runner.invoke(app, ["refresh", "valid.example.com"])

        assert result.exit_code == 0
        assert "Session is valid" in result.output or "valid" in result.output.lower()

    def test_cli_refresh_expired_session(self, tmp_path):
        """Refresh triggers browser login for expired session."""
        with patch(
            "src.utils.session.refresh_session",
        ) as mock_refresh:
            mock_refresh.return_value = {
                "cookies": [{"name": "fresh", "value": "xyz"}],
                "localStorage": {"t": "1"},
            }
            result = runner.invoke(app, ["refresh", "expired.example.com"])

        assert result.exit_code == 0
        mock_refresh.assert_called_once()

    def test_cli_with_silent_flag(self):
        """``--silent`` flag is forwarded to refresh_session."""
        with patch(
            "src.utils.session.refresh_session",
        ) as mock_refresh:
            mock_refresh.return_value = {
                "cookies": [],
                "localStorage": {},
            }
            result = runner.invoke(app, ["refresh", "example.com", "--silent"])

        assert result.exit_code == 0
        mock_refresh.assert_called_once()
        args, kwargs = mock_refresh.call_args
        assert kwargs.get("silent") is True

    def test_cli_with_max_age_flag(self):
        """``--max-age`` flag is forwarded to refresh_session."""
        with patch(
            "src.utils.session.refresh_session",
        ) as mock_refresh:
            mock_refresh.return_value = {
                "cookies": [],
                "localStorage": {},
            }
            result = runner.invoke(
                app, ["refresh", "example.com", "--max-age", "3600"]
            )

        assert result.exit_code == 0
        mock_refresh.assert_called_once()
        args, kwargs = mock_refresh.call_args
        assert kwargs.get("max_age_seconds") == 3600

    def test_cli_refresh_failure_shows_error(self):
        """If refresh raises RuntimeError, CLI shows the message and exits."""
        with patch(
            "src.utils.session.refresh_session",
        ) as mock_refresh:
            mock_refresh.side_effect = RuntimeError("No display available")
            result = runner.invoke(app, ["refresh", "broken.com"])

        assert result.exit_code == 1
        assert "No display available" in result.output

    def test_cli_empty_host_shows_error(self):
        """Empty host argument should show error."""
        with patch(
            "src.utils.session.refresh_session",
            side_effect=ValueError("host must be a non-empty string"),
        ):
            result = runner.invoke(app, ["refresh", ""])

        assert result.exit_code == 1


def monkeypatch_setter(tmpdir):
    """Helper to set SESSIONS_DIR to a temp path via monkeypatch-style setattr."""
    import src.utils.session as session_mod

    session_mod.SESSIONS_DIR = tmpdir  # type: ignore[assignment]


# ---------------------------------------------------------------------------
# CLI integration: llm-proxy start --check-sessions
# ---------------------------------------------------------------------------


class TestStartCheckSessionsCli:
    """Tests for the ``--check-sessions`` flag on the ``start`` command."""

    def test_check_sessions_no_stored_sessions(self):
        """``--check-sessions`` with no stored sessions shows a message."""
        with patch("src.utils.session.list_sessions", return_value=[]):
            with patch(
                "src.proxy.start_server",
            ) as mock_start:
                # Use a unique port so we don't conflict
                result = runner.invoke(
                    app, ["start", "--host", "127.0.0.1", "--port", "19999", "--check-sessions"]
                )

            assert result.exit_code == 0
            assert "No stored sessions" in result.output
            mock_start.assert_called_once()

    def test_check_sessions_with_expired_sessions(self):
        """``--check-sessions`` with expired sessions calls refresh."""
        mock_sessions = ["host1.example.com", "host2.example.com"]

        with patch("src.utils.session.list_sessions", return_value=mock_sessions):
            with patch("src.utils.session.refresh_session") as mock_refresh:
                mock_refresh.side_effect = [
                    {"cookies": [], "localStorage": {}},
                    {"cookies": [], "localStorage": {}},
                ]
                with patch("src.proxy.start_server") as mock_start:
                    result = runner.invoke(
                        app,
                        [
                            "start",
                            "--host",
                            "127.0.0.1",
                            "--port",
                            "19998",
                            "--check-sessions",
                        ],
                    )

        assert result.exit_code == 0
        assert mock_refresh.call_count == 2
        mock_start.assert_called_once()

    def test_check_sessions_with_failed_refresh(self):
        """``--check-sessions`` handles refresh failures gracefully."""
        with patch("src.utils.session.list_sessions", return_value=["broken.com"]):
            with patch(
                "src.utils.session.refresh_session",
                side_effect=RuntimeError("No display"),
            ):
                with patch("src.proxy.start_server") as mock_start:
                    result = runner.invoke(
                        app,
                        [
                            "start",
                            "--host",
                            "127.0.0.1",
                            "--port",
                            "19997",
                            "--check-sessions",
                        ],
                    )

        assert result.exit_code == 0  # Server still starts
        assert "refresh failed" in result.output
        mock_start.assert_called_once()

    def test_start_without_check_sessions_does_not_check(self):
        """Without ``--check-sessions``, no session checking occurs."""
        with patch("src.utils.session.list_sessions") as mock_list:
            with patch("src.proxy.start_server") as mock_start:
                result = runner.invoke(
                    app,
                    ["start", "--host", "127.0.0.1", "--port", "19996"],
                )

        assert result.exit_code == 0
        mock_list.assert_not_called()
        mock_start.assert_called_once()
