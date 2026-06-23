#!/usr/bin/env python3
"""CLI for the free-llm-hack-proxy using Typer and Rich.

Provides four commands:
  - start:    Launch the proxy server
  - chat:     Interactive chat loop
  - models:   List available models
  - test:     Run provider tests with latency metrics
"""

from __future__ import annotations

import time
from typing import Optional

import typer
from rich.console import Console
from rich.live import Live
from rich.panel import Panel
from rich.progress import BarColumn, Progress, SpinnerColumn, TextColumn, TimeElapsedColumn
from rich.prompt import Prompt
from rich.table import Table
from rich.text import Text

app = typer.Typer(
    name="llm-proxy",
    help="🔓 Free LLM Hack Proxy — CLI for managing the proxy server and testing providers",
    rich_markup_mode="rich",
    no_args_is_help=True,
)

console = Console()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _safe_import(module: str, attr: str, fallback: Optional[callable] = None) -> callable:
    """Try to import *attr* from *module*; return *fallback* if it doesn't exist."""
    try:
        mod = __import__(module, fromlist=[attr])
        return getattr(mod, attr, fallback)
    except (ImportError, AttributeError):
        return fallback


# ---------------------------------------------------------------------------
# start — launch the proxy server
# ---------------------------------------------------------------------------

@app.command()
def start(
    host: str = typer.Option("0.0.0.0", "--host", help="Host to bind the server"),
    port: int = typer.Option(8080, "--port", "-p", help="Port to bind the server"),
    reload: bool = typer.Option(False, "--reload", help="Enable auto-reload on code changes"),
    workers: int = typer.Option(1, "--workers", "-w", help="Number of worker processes"),
) -> None:
    """🚀 Start the proxy server.

    Launches the HTTP/HTTPS reverse proxy on the specified host and port.
    """
    console.print(
        Panel.fit(
            f"[bold green]Starting proxy server[/]\n\n"
            f"  [bold]Host:[/]     [yellow]{host}[/]\n"
            f"  [bold]Port:[/]     [yellow]{port}[/]\n"
            f"  [bold]Workers:[/]  [yellow]{workers}[/]\n"
            f"  [bold]Reload:[/]   [yellow]{'on' if reload else 'off'}[/]",
            title="🔓 [bold green]Free LLM Hack Proxy[/]",
            border_style="green",
        )
    )

    start_server = _safe_import("src.proxy", "start_server")
    if start_server is not None:
        start_server(host=host, port=port, workers=workers, reload=reload)
    else:
        console.print(
            "[red]✗[/] Proxy server module not yet implemented.\n"
            "  [dim]Implement [bold]src.proxy.start_server[/] to enable this command.[/]"
        )
        raise typer.Exit(code=1)


# ---------------------------------------------------------------------------
# chat — interactive chat loop
# ---------------------------------------------------------------------------

@app.command()
def chat(
    model: str = typer.Option("default", "--model", "-m", help="Model ID to use"),
    provider: Optional[str] = typer.Option(None, "--provider", help="Provider to route through"),
) -> None:
    """💬 Open an interactive chat session with an LLM provider.

    Messages are sent through the proxy to the configured provider.
    Use [bold]/exit[/] or [bold]/quit[/] to leave the session.
    """
    console.print(
        Panel.fit(
            f"[bold cyan]Interactive Chat Session[/]\n\n"
            f"  [bold]Model:[/]    [yellow]{model}[/]\n"
            f"  [bold]Provider:[/] [yellow]{provider or 'auto'}[/]\n\n"
            "  [dim]Type [bold]/exit[/] to quit • [bold]/model <name>[/] to switch[/]",
            title="💬 [bold cyan]Chat[/]",
            border_style="cyan",
        )
    )

    while True:
        try:
            message = Prompt.ask("[bold blue]You[/]")
        except (KeyboardInterrupt, EOFError):
            console.print("\n[yellow]👋 Goodbye![/]")
            break

        text = message.strip()
        if not text:
            continue

        if text.lower() in ("/exit", "/quit"):
            console.print("[yellow]👋 Goodbye![/]")
            break

        if text.lower().startswith("/model"):
            parts = text.split(maxsplit=1)
            if len(parts) > 1:
                model = parts[1]
                console.print(f"[green]✓[/] Switched to model [bold]{model}[/]")
            else:
                console.print(f"[green]Current model:[/] [bold]{model}[/]")
            continue

        if text.lower().startswith("/provider"):
            parts = text.split(maxsplit=1)
            if len(parts) > 1:
                provider = parts[1]
                console.print(f"[green]✓[/] Switched to provider [bold]{provider}[/]")
            else:
                console.print(f"[green]Current provider:[/] [bold]{provider or 'auto'}[/]")
            continue

        # Attempt to use the providers module for real inference; fall back to stub.
        chat_fn = _safe_import("src.providers", "chat")
        if chat_fn is not None:
            with console.status(f"[cyan]Thinking...[/]", spinner="dots"):
                response = chat_fn(message, model=model, provider=provider)
            console.print(f"[bold green]Assistant:[/] {response}")
        else:
            # Friendly placeholder until providers are wired up
            console.print(
                f"[bold green]Assistant:[/] [italic]🤖 Echo from {model} via "
                f"{provider or 'auto'} — providers module not yet implemented.[/]"
            )


# ---------------------------------------------------------------------------
# models — list available models
# ---------------------------------------------------------------------------

@app.command()
def models(
    provider: Optional[str] = typer.Option(None, "--provider", help="Filter by provider name"),
) -> None:
    """🤖 List available LLM models across all providers.

    Shows model IDs, their provider, and current health status.
    """
    table = Table(
        title="🤖 Available Models",
        show_header=True,
        header_style="bold magenta",
        border_style="bright_magenta",
    )
    table.add_column("Provider", style="cyan", no_wrap=True)
    table.add_column("Model ID", style="white")
    table.add_column("Status", min_width=12)
    table.add_column("Latency (p50)", justify="right", style="dim")

    list_models = _safe_import("src.providers", "list_models")
    if list_models is not None:
        models_data = list_models(provider=provider)
        if not models_data:
            console.print(f"[yellow]No models found[/] for provider [bold]{provider}[/]")
            raise typer.Exit(0)
        for m in models_data:
            status_tag = f"[green]✓ online[/]" if m.get("online") else "[red]✗ offline[/]"
            lat = f"{m.get('latency_ms', '—')}ms" if m.get("latency_ms") else "—"
            table.add_row(m["provider"], m["id"], status_tag, lat)
    else:
        # Fallback stub data
        stub = [
            ("local",      "gpt-4o-mini",       "✓ online", "—"),
            ("local",      "claude-3-haiku",     "✓ online", "—"),
            ("huggingface", "meta-llama/Llama-3.1-8B", "✓ online", "—"),
            ("huggingface", "microsoft/Phi-3-mini",    "✓ online", "—"),
        ]
        for prov, mod, status, lat in stub:
            if provider is None or prov == provider:
                table.add_row(prov, mod, status, lat)

        console.print(
            "[dim]Note: showing stub models — implement [bold]src.providers.list_models[/] "
            "for live data.[/]\n"
        )

    console.print(table)


# ---------------------------------------------------------------------------
# test — run provider tests
# ---------------------------------------------------------------------------

@app.command()
def test(
    provider: Optional[str] = typer.Option(None, "--provider", help="Specific provider to test"),
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Show detailed error output"),
) -> None:
    """🧪 Run connectivity tests against providers and display latency.

    Tests each provider's API endpoint, measures round-trip time,
    and reports pass/fail status.
    """
    console.print(
        Panel.fit(
            "[bold yellow]Running provider tests...[/]",
            title="🧪 [bold yellow]Provider Tests[/]",
            border_style="yellow",
        )
    )

    # Determine which providers to test
    get_providers = _safe_import("src.providers", "get_providers")
    if get_providers is not None:
        providers_to_test = [provider] if provider else get_providers()
    else:
        providers_to_test = [provider] if provider else ["local", "huggingface", "groq"]

    results = Table(show_header=True, header_style="bold yellow", border_style="bright_yellow")
    results.add_column("Provider", style="cyan", no_wrap=True)
    results.add_column("Status", min_width=12)
    results.add_column("Latency", justify="right")
    results.add_column("Error", style="red", max_width=50)

    test_provider = _safe_import("src.providers", "test_provider")

    progress = Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TimeElapsedColumn(),
        console=console,
    )

    with progress:
        task = progress.add_task(
            f"[yellow]Testing {len(providers_to_test)} provider(s)...[/]",
            total=len(providers_to_test),
        )

        for prov in providers_to_test:
            start = time.monotonic()
            try:
                if test_provider is not None:
                    result = test_provider(prov)
                    elapsed = time.monotonic() - start
                    if result.get("ok"):
                        lat = result.get("latency_ms", round(elapsed * 1000))
                        results.add_row(prov, "[green]✓ PASS[/]", f"{lat:.0f}ms", "")
                    else:
                        err = result.get("error", "Unknown error")
                        results.add_row(prov, "[red]✗ FAIL[/]", f"{elapsed*1000:.0f}ms", err[:50] if verbose else "")
                else:
                    # Simulated test — placeholder until providers are wired
                    time.sleep(0.3)
                    elapsed = time.monotonic() - start
                    lat = round(elapsed * 1000)
                    results.add_row(prov, "[green]✓ PASS[/]", f"{lat}ms", "")
            except Exception as exc:
                elapsed = time.monotonic() - start
                results.add_row(prov, "[red]✗ FAIL[/]", f"{elapsed*1000:.0f}ms", str(exc)[:50])

            progress.update(task, advance=1)

    console.print()
    console.print(results)

    if test_provider is None:
        console.print(
            "\n[dim]Note: tests are simulated — implement [bold]src.providers.test_provider[/] "
            "for real results.[/]"
        )


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    app()
