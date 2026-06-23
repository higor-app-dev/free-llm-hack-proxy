#!/usr/bin/env python3
"""CLI for the free-llm-hack-proxy using Typer and Rich.

Six commands:
  - start:         Launch the proxy server
  - login:         Authenticate with a provider
  - browser-login: Interactive browser-based authentication
  - refresh:       Silently refresh an expired browser session
  - status:        Show connection health
  - models:        List available models from a provider
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Optional

import typer
from rich.console import Console
from rich.panel import Panel
from rich.table import Table

from src.config import (
    CONFIG_DIR,
    Config,
    load_config,
    ensure_config_dir,
    get_config,
)
from src.providers import (
    load_all_providers,
    load_provider_config,
)

app = typer.Typer(
    name="llm-proxy",
    help="🔓 Free LLM Hack Proxy — CLI for managing the proxy and providers",
    rich_markup_mode="rich",
    no_args_is_help=True,
)

console = Console()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _ensure_config() -> Config:
    """Ensure config directory exists, load and return config."""
    ensure_config_dir()
    return load_config()


def _provider_table(title: str, border: str) -> Table:
    """Create a standard provider table."""
    table = Table(
        title=title,
        show_header=True,
        header_style=f"bold {border}",
        border_style=f"bright_{border}",
    )
    table.add_column("Provider", style="cyan", no_wrap=True)
    table.add_column("Status", min_width=12)
    table.add_column("Detail", style="white")
    return table


# ---------------------------------------------------------------------------
# start — launch the proxy server
# ---------------------------------------------------------------------------

@app.command()
def start(
    host: Optional[str] = typer.Option(
        None, "--host", help="Override bind address from config"
    ),
    port: Optional[int] = typer.Option(
        None, "--port", "-p", help="Override listen port from config"
    ),
    reload: Optional[bool] = typer.Option(
        None, "--reload/--no-reload", help="Override auto-reload from config"
    ),
    workers: Optional[int] = typer.Option(
        None, "--workers", "-w", help="Override worker count from config"
    ),
    check_sessions: bool = typer.Option(
        False,
        "--check-sessions",
        help="Check all stored sessions on startup and refresh any that are expired",
    ),
) -> None:
    """🚀 Start the proxy server.

    Reads host, port, workers, and reload from the global config
    (``~/.llm-proxy/config.yaml``).  CLI flags override config values.

    Use ``--check-sessions`` to validate all stored browser sessions
    before the proxy starts serving — expired ones are refreshed via the
    interactive browser login.
    """
    cfg = _ensure_config()
    srv = cfg.server

    actual_host = host or srv.host
    actual_port = port or srv.port
    actual_workers = workers if workers is not None else srv.workers
    actual_reload = reload if reload is not None else srv.reload

    # --- Session check on startup ---
    if check_sessions:
        console.print(
            Panel.fit(
                "[bold yellow]Checking stored sessions...[/]",
                border_style="yellow",
            )
        )
        from src.utils.session import (
            list_sessions,
            refresh_session,
        )

        stored_hosts = list_sessions()
        if not stored_hosts:
            console.print(
                "  [yellow]No stored sessions found.[/]  "
                "Use [bold]llm-proxy browser-login <host>[/] first.\n"
            )
        else:
            refreshed: list[str] = []
            valid: list[str] = []
            for stored_host in stored_hosts:
                console.print(f"  Checking [cyan]{stored_host}[/]...", end="")
                try:
                    refresh_session(stored_host, silent=True)
                    console.print(" [green]✓[/]")
                    refreshed.append(stored_host)
                except RuntimeError:
                    console.print(" [yellow]⚠ refresh failed (launch manually)[/]")

            if refreshed:
                console.print(
                    f"\n[green]✓[/] Refreshed {len(refreshed)} session(s): "
                    f"{', '.join(refreshed)}\n"
                )

    console.print(
        Panel.fit(
            f"[bold green]Starting proxy server[/]\n\n"
            f"  [bold]Host:[/]     [yellow]{actual_host}[/]\n"
            f"  [bold]Port:[/]     [yellow]{actual_port}[/]\n"
            f"  [bold]Workers:[/]  [yellow]{actual_workers}[/]\n"
            f"  [bold]Reload:[/]   [yellow]{'on' if actual_reload else 'off'}[/]\n\n"
            f"  [dim]Config:[/]    {CONFIG_DIR / 'config.yaml'}",
            title="🔓 [bold green]Free LLM Hack Proxy[/]",
            border_style="green",
        )
    )

    # Attempt to import and start the real proxy server
    try:
        from src.proxy import start_server as _start
    except ImportError:
        console.print(
            "\n[red]✗[/] Proxy server module not yet implemented.\n"
            "  [dim]Implement [bold]src.proxy.start_server[/] to enable this command.[/]"
        )
        raise typer.Exit(code=1)

    _start(
        host=actual_host,
        port=actual_port,
        workers=actual_workers,
        reload=actual_reload,
    )


# ---------------------------------------------------------------------------
# login — authenticate with a provider
# ---------------------------------------------------------------------------

@app.command()
def login(
    provider: str = typer.Argument(
        ..., help="Provider name (e.g. openai, anthropic, groq)"
    ),
    api_key: Optional[str] = typer.Option(
        None, "--key", "-k", help="API key (prompted if not provided)"
    ),
    set_default: bool = typer.Option(
        False, "--default", help="Also set this provider as the default"
    ),
) -> None:
    """🔑 Authenticate with an LLM provider.

    Saves the API key directly into the provider's YAML config file
    under ``~/.llm-proxy/providers/<name>.yaml``.

    If the provider config does not exist yet, creates one with just
    the name and API key so you can add models later.
    """
    provider_name = provider.strip().lower()
    ensure_config_dir()

    providers_dir = Path(get_config().providers_dir)
    provider_path = providers_dir / f"{provider_name}.yaml"

    # Resolve API key: flag, env var, or prompt
    api_key_value = api_key
    if not api_key_value:
        env_key = f"LLM_PROXY_{provider_name.upper().replace('-', '_')}_API_KEY"
        api_key_value = os.environ.get(env_key)
    if not api_key_value:
        api_key_value = typer.prompt(
            f"API key for {provider_name}",
            hide_input=True,
        )

    # Load existing config or create a new one
    from src.providers.models import ProviderConfig

    base_url_for_validation: Optional[str] = None

    if provider_path.exists():
        existing = load_provider_config(provider_name, reload=True)
        existing.api_key = api_key_value
        existing.to_yaml(provider_path)
        base_url_for_validation = existing.base_url
        console.print(
            f"[green]✓[/] Updated API key for [bold]{provider_name}[/] "
            f"in [dim]{provider_path}[/]"
        )
    else:
        new_cfg = ProviderConfig(
            name=provider_name,
            api_key=api_key_value,
            description=f"{provider_name} (auto-configured)",
        )
        new_cfg.to_yaml(provider_path)
        base_url_for_validation = new_cfg.base_url
        console.print(
            f"[green]✓[/] Created provider config [bold]{provider_name}[/] "
            f"with API key in [dim]{provider_path}[/]"
        )

    # Validate the key with a lightweight test
    console.print(f"\n[yellow]Testing {provider_name} API key...[/]")
    try:
        import httpx

        if base_url_for_validation and "api.openai.com" in base_url_for_validation:
            resp = httpx.get(
                "https://api.openai.com/v1/models",
                headers={"Authorization": f"Bearer {api_key_value}"},
                timeout=10,
            )
        elif base_url_for_validation and "api.anthropic.com" in base_url_for_validation:
            resp = httpx.get(
                "https://api.anthropic.com/v1/models",
                headers={
                    "x-api-key": api_key_value,
                    "anthropic-version": "2023-06-01",
                },
                timeout=10,
            )
        else:
            # Generic test — just hit the base_url
            resp = httpx.get(
                base_url_for_validation or provider_name,
                headers={
                    "Authorization": f"Bearer {api_key_value}",
                    "Content-Type": "application/json",
                },
                timeout=10,
            )

        if resp.status_code < 500:
            console.print(f"[green]✓[/] API key validated — server returned {resp.status_code}")
        else:
            console.print(
                f"[yellow]⚠[/] Key saved but validation returned "
                f"{resp.status_code} — check if the key is correct"
            )
    except Exception as exc:
        console.print(
            f"[yellow]⚠[/] Key saved but could not validate: {exc}\n"
            f"  [dim]The key is stored; validation requires network access.[/]"
        )

    if set_default:
        _update_default_provider(provider_name)
        console.print(f"[green]✓[/] Set [bold]{provider_name}[/] as the default provider")


def _update_default_provider(name: str) -> None:
    """Update the global config's default_provider field."""
    config_path = CONFIG_DIR / "config.yaml"
    import yaml

    if config_path.exists():
        raw = config_path.read_text(encoding="utf-8").strip()
        data = yaml.safe_load(raw) or {} if raw else {}
    else:
        data = {}

    data["default_provider"] = name
    config_path.write_text(
        yaml.dump(data, default_flow_style=False, sort_keys=False),
        encoding="utf-8",
    )


# ---------------------------------------------------------------------------
# browser-login — interactive browser-based authentication
# ---------------------------------------------------------------------------


@app.command(name="browser-login")
def browser_login(
    host_or_url: str = typer.Argument(
        ..., help="Hostname (e.g. chatgpt.com) or full URL to log in to"
    ),
    provider: Optional[str] = typer.Option(
        None,
        "--provider",
        "-p",
        help="Provider name — use its base_url instead of the raw host/URL",
    ),
    save_as: Optional[str] = typer.Option(
        None,
        "--save-as",
        help="Session filename stem (defaults to hostname from the URL)",
    ),
) -> None:
    """🌐 Log in via browser and save the session state (cookies + localStorage).

    Opens a Chromium window at the given URL so you can log in
    interactively.  When you press **Enter** in the terminal, the cookies
    and ``localStorage`` are captured and persisted to
    ``~/.llm-proxy/sessions/<host>.json`` for later reuse.
    """
    from src.cli.browser_login import capture_storage_state_interactive
    from src.utils.session import save_storage_state

    # Resolve the actual URL to navigate to
    resolved_host: str = host_or_url

    if provider:
        try:
            from src.providers import load_provider_config

            prov = load_provider_config(provider)
            if prov.base_url:
                resolved_host = prov.base_url
                console.print(
                    f"  Using [bold]{provider}[/] provider URL: "
                    f"[cyan]{resolved_host}[/cyan]"
                )
            else:
                console.print(
                    f"  [yellow]Provider {provider} has no base_url;[/] "
                    f"falling back to [cyan]{host_or_url}[/cyan]"
                )
        except (FileNotFoundError, ValueError) as exc:
            console.print(f"  [red]Could not load provider {provider}: {exc}[/red]")
            raise typer.Exit(code=1)

    console.print(
        Panel.fit(
            "[bold yellow]Browser login[/]\n\n"
            f"  URL: [cyan]{resolved_host}[/cyan]\n\n"
            "  A Chromium window will open. Log in there, then\n"
            "  come back to this terminal and press Enter.\n",
            border_style="yellow",
        )
    )

    try:
        state = capture_storage_state_interactive(resolved_host)
    except RuntimeError as exc:
        console.print(f"\n[red]✗[/] {exc}")
        raise typer.Exit(code=1)

    # Determine the session filename stem
    from urllib.parse import urlparse

    parsed = urlparse(state.get("origin", resolved_host))
    session_key = (
        save_as
        or parsed.hostname
        or state.get("origin", "")
        .replace("https://", "")
        .replace("http://", "")
        .split("/")[0]
        or "unknown"
    )

    # Strip the storage state down to the keys we persist
    persist = {
        "cookies": state.get("cookies", []),
        "localStorage": state.get("localStorage", {}),
    }

    path = save_storage_state(session_key, persist)
    cookie_count = len(persist["cookies"])
    ls_count = len(persist["localStorage"])

    console.print()
    console.print(
        Panel.fit(
            f"[bold green]✓ Session saved[/bold green]\n\n"
            f"  File:   [dim]{path}[/dim]\n"
            f"  Host:   [cyan]{session_key}[/cyan]\n"
            f"  Origin: [cyan]{state.get('origin', '—')}[/cyan]\n"
            f"  Cookies:  [green]{cookie_count}[/green]\n"
            f"  Storage keys: [green]{ls_count}[/green]\n",
            border_style="green",
        )
    )


# ---------------------------------------------------------------------------
# status — show connection health
# ---------------------------------------------------------------------------

@app.command()
def status(
    verbose: bool = typer.Option(
        False, "--verbose", "-v", help="Show detailed provider health"
    ),
) -> None:
    """📊 Show the current system status.

    Displays config health, loaded providers, API key availability,
    and optional per-provider test results.
    """
    # --- Config Health ---
    cfg = _ensure_config()

    config_file = CONFIG_DIR / "config.yaml"
    config_status = "[green]✓ present[/]" if config_file.exists() else "[yellow]⚠ using defaults[/]"

    table = Table(
        title="📊 System Status",
        show_header=True,
        header_style="bold cyan",
        border_style="cyan",
    )
    table.add_column("Component", style="bold white", no_wrap=True)
    table.add_column("Status", min_width=14)
    table.add_column("Details", style="white")

    table.add_row("Config file", config_status, str(config_file))
    table.add_row(
        "Default provider",
        "[green]✓ set[/]" if cfg.default_provider else "[yellow]⚠ auto[/]",
        cfg.default_provider or "auto-detect on first request",
    )
    table.add_row("Server", "[green]✓ configured[/]", f"{cfg.server.host}:{cfg.server.port}")
    table.add_row("Logging", "[green]✓ configured[/]", f"level={cfg.logging.level}")
    table.add_row("Cache", "[green]✓ configured[/]", f"{cfg.cache.backend} (ttl={cfg.cache.ttl_seconds}s)")

    console.print(table)

    # --- Provider Health ---
    providers = load_all_providers()
    if not providers:
        console.print(
            "\n[yellow]⚠ No provider configurations found.[/]\n"
            "  Use [bold]llm-proxy login <provider>[/] to add one, or\n"
            "  place YAML files in [dim]~/.llm-proxy/providers/[/]"
        )
    else:
        ptable = _provider_table("📡 Providers", "green")
        for name in sorted(providers):
            cfg_prov = providers[name]
            key = cfg_prov.resolve_api_key()
            if key:
                masked = key[:6] + "****" + key[-4:] if len(key) > 12 else "****"
                key_status = f"[green]✓[/] key: {masked}"
            else:
                key_status = "[yellow]⚠ no API key[/]"

            model_count = len(cfg_prov.models)
            detail = f"{model_count} model(s) | {cfg_prov.description or ''}"
            ptable.add_row(name, key_status, detail.strip())

        console.print()
        console.print(ptable)

    # --- Verbose: test each provider ---
    if verbose and providers:
        console.print("\n[yellow]Running provider connectivity tests...[/]")
        import httpx
        import time

        for name in sorted(providers):
            cfg_prov = providers[name]
            key = cfg_prov.resolve_api_key()
            base = cfg_prov.base_url

            if not key:
                console.print(f"  [dim]{name}:[/] [yellow]skipped (no API key)[/]")
                continue

            try:
                start = time.monotonic()
                headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
                resp = httpx.get(base or "https://httpbin.org/get", headers=headers, timeout=10)
                elapsed = round((time.monotonic() - start) * 1000)
                if resp.status_code < 500:
                    console.print(
                        f"  [green]{name}:[/] ✓ {resp.status_code} in {elapsed}ms"
                    )
                else:
                    console.print(
                        f"  [red]{name}:[/] ✗ {resp.status_code} in {elapsed}ms"
                    )
            except Exception as exc:
                console.print(f"  [red]{name}:[/] ✗ {exc}")


# ---------------------------------------------------------------------------
# models — list available models from providers
# ---------------------------------------------------------------------------

@app.command()
def models(
    provider: Optional[str] = typer.Option(
        None, "--provider", help="Filter by provider name"
    ),
    verbose: bool = typer.Option(
        False, "--verbose", "-v", help="Show model details (tokens, cost, features)"
    ),
) -> None:
    """🤖 List available LLM models across all providers.

    Shows model IDs, their provider, capabilities, and cost information
    as defined in the per-provider YAML config files.
    """
    providers = load_all_providers()

    if not providers:
        console.print(
            "[yellow]No provider configurations found.[/]\n"
            "  Add providers via [bold]llm-proxy login <name>[/] or\n"
            "  place YAML files in [dim]~/.llm-proxy/providers/[/]"
        )
        raise typer.Exit(0)

    table = Table(
        title="🤖 Available Models",
        show_header=True,
        header_style="bold magenta",
        border_style="bright_magenta",
    )
    table.add_column("Provider", style="cyan", no_wrap=True)
    table.add_column("Model ID", style="white")
    if verbose:
        table.add_column("Max Tokens", justify="right", style="dim")
        table.add_column("Streaming", justify="center")
        table.add_column("Vision", justify="center")
        table.add_column("Functions", justify="center")
        table.add_column("Cost/1K in", justify="right", style="green")
        table.add_column("Cost/1K out", justify="right", style="green")
    else:
        table.add_column("Capabilities", style="dim")

    total_models = 0
    for name in sorted(providers):
        if provider and name != provider:
            continue
        cfg_prov = providers[name]
        for m in cfg_prov.models:
            total_models += 1
            if verbose:
                cost_in = f"${m.cost_per_1k_input:.4f}" if m.cost_per_1k_input is not None else "—"
                cost_out = f"${m.cost_per_1k_output:.4f}" if m.cost_per_1k_output is not None else "—"
                table.add_row(
                    name,
                    m.id,
                    str(m.max_tokens),
                    "✓" if m.supports_streaming else "—",
                    "✓" if m.supports_vision else "—",
                    "✓" if m.supports_functions else "—",
                    cost_in,
                    cost_out,
                )
            else:
                features = []
                if m.supports_streaming:
                    features.append("stream")
                if m.supports_vision:
                    features.append("vision")
                if m.supports_functions:
                    features.append("functions")
                caps = ", ".join(features) if features else "—"
                table.add_row(name, m.id, f"[dim]{caps}[/]")

    if total_models == 0:
        if provider:
            console.print(f"[yellow]No models found[/] for provider [bold]{provider}[/]")
        else:
            console.print("[yellow]No models defined in any provider config.[/]")
        raise typer.Exit(0)

    console.print(table)
    console.print(f"\n[dim]Total: {total_models} model(s) across {len(providers)} provider(s)[/]")


# ---------------------------------------------------------------------------
# refresh — silently refresh an expired session
# ---------------------------------------------------------------------------


@app.command(name="refresh")
def refresh(
    host: str = typer.Argument(
        ..., help="Host name whose session should be refreshed (e.g. chatgpt.com)"
    ),
    silent: bool = typer.Option(
        False,
        "--silent",
        help="Suppress stdout messages (log only)",
    ),
    max_age: Optional[int] = typer.Option(
        None,
        "--max-age",
        help="Optional wall-clock age limit in seconds before considering the session expired",
    ),
) -> None:
    """🔄 Refresh an expired or missing browser session.

    Checks whether the stored session for *host* is still valid.  If it
    has expired or does not exist, opens a Chromium window so you can
    log in again.  The fresh cookies and ``localStorage`` are saved
    automatically — no error is raised when the session was fine.

    Examples::

        llm-proxy refresh chatgpt.com
        llm-proxy refresh api.openai.com --silent
        llm-proxy refresh groq.com --max-age 3600
    """
    from src.utils.session import refresh_session as _refresh

    console.print(
        Panel.fit(
            f"[bold]Session refresh[/]\n\n"
            f"  Host: [cyan]{host}[/cyan]\n"
            f"  Checking session validity...\n",
            border_style="blue",
        )
    )

    try:
        state = _refresh(host, silent=silent, max_age_seconds=max_age)
    except RuntimeError as exc:
        console.print(f"\n[red]✗[/] {exc}")
        raise typer.Exit(code=1)
    except ValueError as exc:
        console.print(f"\n[red]✗[/] {exc}")
        raise typer.Exit(code=1)

    cookie_count = len(state.get("cookies", []))
    ls_count = len(state.get("localStorage", {}))

    if not silent:
        console.print()
        console.print(
            Panel.fit(
                f"[bold green]✓ Session is valid[/bold green]\n\n"
                f"  Host:    [cyan]{host}[/cyan]\n"
                f"  Cookies: [green]{cookie_count}[/green]\n"
                f"  Storage keys: [green]{ls_count}[/green]\n",
                border_style="green",
            )
        )


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    app()
