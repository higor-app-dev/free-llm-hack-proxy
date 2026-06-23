# Session Data Model — Format Specification

> **Version:** 1.0.0  
> **Module:** `internal/session`  
> **Store:** `~/.llm-proxy/sessions/<provider-host>.json`  
> **Tenant:** `free-llm-hack-proxy`

## Overview

Browser sessions for LLM provider authentication (DeepSeek, MiMo, etc.) are
persisted to disk as JSON files. Each file captures the cookies and
localStorage state of a browser context after a successful interactive login.

The session file is the **single source of truth** for auth material: providers
restore it into headless browser contexts before making chat requests, and
validate it before deciding whether re-login is needed.

## File Location

| Path | Example |
|---|---|
| `~/.llm-proxy/sessions/<provider-host>.json` | `~/.llm-proxy/sessions/chat.deepseek.com.json` |
| | `~/.llm-proxy/sessions/aistudio.xiaomimimo.com.json` |

The `SessionsDir()` function resolves to `$HOME/.llm-proxy/sessions/` and
creates the directory tree if missing. Files are written with `0600` permissions.

## Schema

### Root object: `SessionData`

```json
{
  "cookies":        CookieEntry[],
  "localStorage":   map[string]string,
  "metadata":       SessionMetadata
}
```

### `CookieEntry`

Maps to Chromium's native cookie store attributes — one cookie per entry.

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | **yes** | Cookie name (e.g. `"cloud_session"`, `"serviceToken"`) |
| `value` | `string` | **yes** | Cookie value (sensitive — stored as plaintext, protect `~/.llm-proxy/`) |
| `domain` | `string` | **yes** | Cookie domain (may include leading dot, e.g. `".deepseek.com"`) |
| `path` | `string` | **yes** | URL path scope (typically `"/"`) |
| `expires` | `int64` | **yes** | Unix epoch (seconds) when the cookie expires. `0` or `-1` = session cookie (browser close) |
| `httpOnly` | `bool` | **yes** | `true` = inaccessible from JavaScript (`document.cookie`) |
| `secure` | `bool` | **yes** | `true` = HTTPS only |
| `sameSite` | `string` | no | `"Lax"`, `"Strict"`, or `"None"`. Omitted when empty |

**Validation rules:**

- `name` must be non-empty and unique within the session's cookie array.
- A cookie with `expires <= 0` is a **session cookie** (no fixed expiry — its
  lifetime is bound to the browser process).
- When ALL persistent cookies (`expires > 0`) in a session have expired, the
  session itself is considered expired.

### `SessionMetadata`

Provenance and lifecycle information for the captured session.

| Field | Type | Required | Description |
|---|---|---|---|
| `provider` | `string` | **yes** | Short provider name (e.g. `"deepseek"`, `"mimo"`) |
| `provider_host` | `string` | **yes** | Domain used as the session file key (e.g. `"chat.deepseek.com"`) |
| `created_at` | `int64` | **yes** | Unix epoch (seconds) when the session was captured |
| `expires_at` | `int64` | no | Unix epoch (seconds) of absolute expiry. `0` = no limit |
| `expires_after_seconds` | `int64` | no | Relative expiry: session is stale after this many seconds from `created_at`. `0` = no limit |
| `refresh_token` | `string` | no | OAuth refresh token (provider-dependent) |
| `provider_version` | `string` | no | Semver of the provider code that captured this session |

**Expiry precedence (high to low):**

1. **`expires_at`** — absolute timestamp. If `expires_at > 0` and
   `now >= expires_at`, session is expired.
2. **`expires_after_seconds`** — relative. If `expires_after_seconds > 0` and
   `now - created_at >= expires_after_seconds`, session is expired.
3. **Cookie-level expiry** — if every persistent cookie is individually expired,
   the session is expired.

### `localStorage` (map)

Flat key-value pairs mirroring the browser's `localStorage` API.

| Key | Value | Purpose |
|---|---|---|
| `"token"` | JWT string | Auth token from the provider |
| `"user"` / `"userInfo"` | JSON string | Serialised user profile |
| `"settings"` | JSON string | User preferences (theme, language) |
| *any other key* | *any string* | Provider-specific state |

Values are always strings. Objects are stored as JSON-serialised strings
(consistent with the browser's `JSON.stringify()` behaviour).

## Example: DeepSeek (chat.deepseek.com)

```json
{
  "cookies": [
    {
      "name": "cloud_session",
      "value": "abc123def456ghi789jkl012mno345pqr678stu901vwx234yz",
      "domain": ".deepseek.com",
      "path": "/",
      "expires": 1760731860,
      "httpOnly": true,
      "secure": true,
      "sameSite": "Lax"
    },
    {
      "name": "sessionid",
      "value": "sess_01J2XYZ789ABCDEFGHIJKLMNOP",
      "domain": "chat.deepseek.com",
      "path": "/",
      "expires": 1760731860,
      "httpOnly": true,
      "secure": true,
      "sameSite": "Lax"
    },
    {
      "name": "auth_token",
      "value": "eyJhbG...sw5c",
      "domain": ".deepseek.com",
      "path": "/api",
      "expires": 1760731860,
      "httpOnly": false,
      "secure": true,
      "sameSite": "Strict"
    },
    {
      "name": "user_pref",
      "value": "lang=en&theme=dark",
      "domain": "chat.deepseek.com",
      "path": "/",
      "expires": -1,
      "httpOnly": false,
      "secure": false,
      "sameSite": "Lax"
    }
  ],
  "localStorage": {
    "token": "eyJhbG...mple",
    "user": "{\"id\":42,\"name\":\"John Doe\",\"email\":\"john@example.com\"}",
    "settings": "{\"theme\":\"dark\",\"language\":\"en\"}",
    "ui_state": "{\"sidebar_open\":true}"
  },
  "metadata": {
    "provider": "deepseek",
    "provider_host": "chat.deepseek.com",
    "created_at": 1718563200,
    "expires_at": 1760731860,
    "expires_after_seconds": 43200000,
    "refresh_token": "rt_ds_refresh_abc123def456ghi789",
    "provider_version": "0.1.0"
  }
}
```

## Example: MiMo (aistudio.xiaomimimo.com)

```json
{
  "cookies": [
    {
      "name": "serviceToken",
      "value": "ST_abc123def456ghi789jkl012",
      "domain": ".xiaomimimo.com",
      "path": "/",
      "expires": 1760731860,
      "httpOnly": true,
      "secure": true,
      "sameSite": "Lax"
    },
    {
      "name": "session",
      "value": "sess_xm_01J2XYZ789ABCDEFGHIJKLMNOP",
      "domain": ".account.xiaomi.com",
      "path": "/",
      "expires": 1760731860,
      "httpOnly": true,
      "secure": true,
      "sameSite": "Lax"
    },
    {
      "name": "userId",
      "value": "1234567890",
      "domain": ".xiaomimimo.com",
      "path": "/",
      "expires": 1760731860,
      "httpOnly": false,
      "secure": false,
      "sameSite": "Lax"
    }
  ],
  "localStorage": {
    "token": "xm_jwt_eyJhbG...mple",
    "userInfo": "{\"userId\":1234567890,\"nickname\":\"MiMoUser\"}",
    "settings": "{\"theme\":\"light\",\"language\":\"zh\"}",
    "recent_models": "[\"MiMo-v2.5-pro\",\"MiMo-v2.5-flash\"]"
  },
  "metadata": {
    "provider": "mimo",
    "provider_host": "aistudio.xiaomimimo.com",
    "created_at": 1718563200,
    "expires_at": 1760731860,
    "expires_after_seconds": 43200000,
    "refresh_token": "",
    "provider_version": "0.1.0"
  }
}
```

## Auth Validation

Providers validate sessions by checking for **known auth indicators** — cookie
names and localStorage keys defined in each provider's `dom.yaml` under the
`session.cookie_indicators` and `session.local_storage_indicators` sections.

The `SessionData` struct provides two helpers for this:

| Method | Returns | Purpose |
|---|---|---|
| `AuthCookieCount(indicatorNames)` | `int` | How many auth-suggesting cookies are present |
| `AuthLocalStorageCount(indicatorKeys)` | `int` | How many auth-suggesting localStorage keys are present |

A session is considered **active** (login succeeded) when at least one
indicator is present in either cookies or localStorage. Providers typically
require at least one matching cookie name AND one matching localStorage key.

## Expiry Model

```
                    ┌── IsExpired() ──┐
                    ▼                 ▼
            metadata.expires_at     metadata.expires_after_seconds
                    │                       │
                    │              now - created_at >= value?
                    ▼                       ▼
            now >= expires_at?         true = expired
                    │
                    ▼
         all persistent cookies expired?
                    │
              ┌─────┴─────┐
            yes           no
              │            │
          expired      not expired
```

At each level a `true` short-circuits (session is expired). If all levels
pass (or are absent / `0`), the session is considered not expired by this
check.

## Go Package API

All functions and types are in `package session` at `internal/session/`.

| Function / Method | Signature | Description |
|---|---|---|
| `SessionsDir()` | `() (string, error)` | Resolve & create `~/.llm-proxy/sessions/` |
| `SessionPath(host)` | `(string) (string, error)` | Full path to `<host>.json` |
| `Save(s)` | `(*SessionData) error` | Atomic JSON write |
| `Load(host)` | `(string) (*SessionData, error)` | Read & parse JSON |
| `Exists(host)` | `(string) (bool, error)` | File exists on disk? |
| `Delete(host)` | `(string) error` | Remove file (idempotent) |
| `List()` | `() ([]string, error)` | All stored session hosts |
| `IsExpired(host)` | `(string) bool` | Convenience: Load + check |
| `SessionData.IsExpired()` | `() bool` | Multi-level expiry check |
| `SessionData.AuthCookieCount(names)` | `(map[string]bool) int` | Matching auth cookies |
| `SessionData.AuthLocalStorageCount(keys)` | `(map[string]bool) int` | Matching auth LS keys |
| `CookieEntry.IsExpired()` | `() bool` | Cookie-level expiry |
| `CookieEntry.IsSessionCookie()` | `() bool` | Session cookie (no expiry) |
| `SessionMetadata.IsExpired()` | `() bool` | Metadata-level expiry |
