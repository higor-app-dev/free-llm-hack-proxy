"""Tests for ``src.proxy`` — proxy server and healthcheck endpoint."""

from __future__ import annotations

from fastapi.testclient import TestClient

from src.proxy import app


class TestHealthEndpoint:
    """GET /health — Docker HEALTHCHECK endpoint."""

    def test_health_returns_200(self) -> None:
        """/health should return 200 OK with {"status":"ok"}."""
        client = TestClient(app)
        resp = client.get("/health")

        assert resp.status_code == 200
        assert resp.json() == {"status": "ok"}

    def test_health_content_type_is_json(self) -> None:
        """Response Content-Type should be application/json."""
        client = TestClient(app)
        resp = client.get("/health")

        assert resp.headers.get("content-type") == "application/json"

    def test_health_no_auth_required(self) -> None:
        """/health should work without any auth headers."""
        client = TestClient(app)
        resp = client.get("/health")

        # No Authorization header sent — must still return 200
        assert resp.status_code == 200

    def test_health_accepts_get_only(self) -> None:
        """Verify POST/PUT/DELETE are rejected (method not allowed)."""
        client = TestClient(app)

        for method in ("post", "put", "delete", "patch"):
            resp = getattr(client, method)("/health")
            assert resp.status_code in (405,), f"{method.upper()} /health should be 405"
