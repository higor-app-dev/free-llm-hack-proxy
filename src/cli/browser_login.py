#!/usr/bin/env python3
"""Browser-based interactive login — captures storage state via Playwright.

Launches a headful Chromium window navigated to a target URL, waits for
the user to complete their login interactively, then captures cookies
and ``localStorage`` so they can be persisted by the session module.

Typical usage from the CLI::

    llm-proxy browser-login chatgpt.com

This module also exports the raw capture function so other code (tests,
scripts, the proxy itself) can reuse the browser-capture logic.
"""

from __future__ import annotations

import logging
from typing import Any

logger = logging.getLogger(__name__)


def capture_storage_state_interactive(host_or_url: str) -> dict[str, Any]:
    """Launch a headful browser, let the user log in, capture storage state.

    A Chromium window opens and navigates to *host_or_url*.  The user
    completes their login in the browser, then presses **Enter** in the
    terminal.  The function reads all cookies and ``localStorage`` from
    the page's origin and returns them as a dict.

    Parameters
    ----------
    host_or_url :
        Hostname (e.g. ``"chatgpt.com"``) or full URL (e.g.
        ``"https://platform.openai.com/login"``).  If a bare hostname is
        given, ``https://`` is prepended automatically.

    Returns
    -------
    dict
        Storage state with keys:

        - ``cookies`` (list[dict]): Browser cookies from the page origin.
        - ``localStorage`` (dict[str, str]): Key-value pairs from the
          page's ``window.localStorage``.
        - ``origin`` (str): The resolved page origin (useful for choosing
          the session filename).

    Raises
    ------
    RuntimeError
        If Playwright cannot launch the browser (no display, missing
        Chromium binary, etc.).
    """
    url = (
        host_or_url
        if host_or_url.startswith(("http://", "https://"))
        else f"https://{host_or_url}"
    )

    try:
        from playwright.sync_api import sync_playwright
    except ImportError as exc:
        raise RuntimeError(
            "Playwright is not installed.  Run:\n"
            "  pip install playwright && playwright install chromium"
        ) from exc

    with sync_playwright() as pw:
        try:
            browser = pw.chromium.launch(headless=False)
        except Exception as exc:
            raise RuntimeError(
                f"Could not launch Chromium: {exc}\n\n"
                "Make sure you have a display available (X11/Wayland) and\n"
                "run:  playwright install chromium"
            ) from exc

        context = browser.new_context()
        page = context.new_page()

        try:
            page.goto(url, wait_until="domcontentloaded")
        except Exception as exc:
            browser.close()
            raise RuntimeError(
                f"Could not navigate to {url}: {exc}"
            ) from exc

        print()
        print(f"  Browser opened to [cyan]{url}[/cyan]")
        print("  Please complete your login in the browser window.")
        input("  Press Enter when login is complete... ")

        # --- Capture cookies + localStorage ---
        storage_state = context.storage_state()
        localStorage_raw = page.evaluate(
            "() => JSON.parse(JSON.stringify(window.localStorage))"
        )
        origin = page.evaluate("() => document.location.origin")

        browser.close()

        return {
            "cookies": storage_state.get("cookies", []),
            "localStorage": localStorage_raw or {},
            "origin": origin,
        }
