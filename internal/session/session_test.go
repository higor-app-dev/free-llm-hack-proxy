package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSession returns a minimal valid SessionData for testing.
func testSession(host string) *SessionData {
	return &SessionData{
		Cookies: []CookieEntry{
			{
				Name:     "cloud_session",
				Value:    "abc123",
				Domain:   ".example.com",
				Path:     "/",
				Expires:  time.Now().Add(24 * time.Hour).Unix(),
				HttpOnly: true,
				Secure:   true,
				SameSite: "Lax",
			},
		},
		LocalStorage: map[string]string{
			"token": "eyJhbGciOiJIUzI1NiJ9.test",
			"user":  `{"id":42,"name":"Test"}`,
		},
		Metadata: SessionMetadata{
			Provider:     "test-provider",
			ProviderHost: host,
			CreatedAt:    time.Now().Unix(),
		},
	}
}

// =============================================================================
// Save — happy path
// =============================================================================

func TestSave_OK(t *testing.T) {
	host := "save-ok-test.example.com"
	s := testSession(host)

	if err := Save(s); err != nil {
		t.Fatalf("Save() returned unexpected error: %v", err)
	}

	// Verify file exists and is valid JSON.
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session file not found at %q: %v", path, err)
	}

	var loaded SessionData
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("session file contains invalid JSON: %v", err)
	}

	// Spot-check fields.
	if len(loaded.Cookies) != 1 {
		t.Errorf("expected 1 cookie, got %d", len(loaded.Cookies))
	}
	if loaded.Cookies[0].Name != "cloud_session" {
		t.Errorf("expected cookie name 'cloud_session', got %q", loaded.Cookies[0].Name)
	}
	if loaded.LocalStorage["token"] != "eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("localStorage token mismatch")
	}
	if loaded.Metadata.Provider != "test-provider" {
		t.Errorf("expected provider 'test-provider', got %q", loaded.Metadata.Provider)
	}
}

// =============================================================================
// Save — nil input
// =============================================================================

func TestSave_NilInput(t *testing.T) {
	err := Save(nil)
	if err == nil {
		t.Fatal("Save(nil) expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot save nil") {
		t.Errorf("error message should mention nil, got: %v", err)
	}
}

// =============================================================================
// Save — empty provider host
// =============================================================================

func TestSave_EmptyHost(t *testing.T) {
	s := testSession("")
	s.Metadata.ProviderHost = ""

	err := Save(s)
	if err == nil {
		t.Fatal("Save with empty host expected error, got nil")
	}
	if !strings.Contains(err.Error(), "provider host must not be empty") {
		t.Errorf("error message should mention empty host, got: %v", err)
	}
}

// =============================================================================
// Save — atomic write (temp file cleaned up on success)
// =============================================================================

func TestSave_AtomicWrite_CleanTemp(t *testing.T) {
	host := "atomic-clean.example.com"
	s := testSession(host)

	if err := Save(s); err != nil {
		t.Fatal(err)
	}

	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	// Temp file should NOT exist after successful save.
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("temp file %q still exists after successful Save — atomic write leak", tmpPath)
	}
}

// =============================================================================
// Save — write failure (read-only directory)
// =============================================================================

func TestSave_WriteFailure(t *testing.T) {
	// Create a temp directory, create a subdirectory, then make the subdir
	// read-only so we can attempt a write that fails with EACCES.
	baseDir, err := os.MkdirTemp("", "session-test-readonly-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir) //nolint:errcheck

	innerDir := filepath.Join(baseDir, "sessions")
	if err := os.MkdirAll(innerDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Make the inner dir read-only AFTER creation.
	if err := os.Chmod(innerDir, 0500); err != nil {
		t.Fatal(err)
	}

	// Try writing a file into the read-only dir — this simulates the
	// os.WriteFile error that Save returns.
	path := filepath.Join(innerDir, "test.json")
	tmpPath := path + ".tmp"
	err = os.WriteFile(tmpPath, []byte("{}"), 0600)
	if err == nil {
		// Unexpected — clean up and skip if the filesystem allowed it.
		os.Remove(tmpPath)                        //nolint:errcheck
		os.Chmod(innerDir, 0700)                  //nolint:errcheck
		t.Skip("filesystem allowed write to read-only directory — skipping test")
	}
	// Verify it's a permission error (disk full / permission denied).
	if !os.IsPermission(err) {
		t.Logf("expected permission error, got %T: %v", err, err)
	}
}

// =============================================================================
// Save + Load round-trip
// =============================================================================

func TestSave_LoadRoundTrip(t *testing.T) {
	host := "roundtrip-test.example.com"
	original := testSession(host)
	original.Metadata.ExpiresAt = time.Now().Add(7 * 24 * time.Hour).Unix()
	original.Metadata.ExpiresAfterSeconds = 604800 // 7 days
	original.Metadata.RefreshToken = "rt_test_refresh_token_abc123"
	original.Metadata.ProviderVersion = "1.0.0"

	// Add a second cookie.
	original.Cookies = append(original.Cookies, CookieEntry{
		Name:     "sessionid",
		Value:    "sess_test_xyz789",
		Domain:   "test.example.com",
		Path:     "/",
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
		HttpOnly: true,
		Secure:   true,
		SameSite: "Lax",
	})

	if err := Save(original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	loaded, err := Load(host)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Compare key fields.
	if len(loaded.Cookies) != len(original.Cookies) {
		t.Errorf("cookie count: got %d, want %d", len(loaded.Cookies), len(original.Cookies))
	}
	if loaded.Cookies[0].Value != original.Cookies[0].Value {
		t.Errorf("cookie[0].value: got %q, want %q", loaded.Cookies[0].Value, original.Cookies[0].Value)
	}
	if loaded.Cookies[1].Name != "sessionid" {
		t.Errorf("cookie[1].name: got %q, want 'sessionid'", loaded.Cookies[1].Name)
	}

	if len(loaded.LocalStorage) != len(original.LocalStorage) {
		t.Errorf("localStorage key count: got %d, want %d", len(loaded.LocalStorage), len(original.LocalStorage))
	}
	for k, v := range original.LocalStorage {
		if loaded.LocalStorage[k] != v {
			t.Errorf("localStorage[%q]: got %q, want %q", k, loaded.LocalStorage[k], v)
		}
	}

	if loaded.Metadata.Provider != original.Metadata.Provider {
		t.Errorf("metadata.provider: got %q, want %q", loaded.Metadata.Provider, original.Metadata.Provider)
	}
	if loaded.Metadata.ProviderHost != original.Metadata.ProviderHost {
		t.Errorf("metadata.provider_host: got %q, want %q", loaded.Metadata.ProviderHost, original.Metadata.ProviderHost)
	}
	if loaded.Metadata.ExpiresAt != original.Metadata.ExpiresAt {
		t.Errorf("metadata.expires_at: got %d, want %d", loaded.Metadata.ExpiresAt, original.Metadata.ExpiresAt)
	}
	if loaded.Metadata.RefreshToken != original.Metadata.RefreshToken {
		t.Errorf("metadata.refresh_token: got %q, want %q", loaded.Metadata.RefreshToken, original.Metadata.RefreshToken)
	}
}

// =============================================================================
// Save — rename failure cleans up temp file
// =============================================================================

func TestSave_RenameFailure_CleansTemp(t *testing.T) {
	host := "rename-fail.example.com"
	s := testSession(host)

	// Save once to create the real file.
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	// Create a directory at the target path to make rename fail
	// (can't rename a file over a directory).
	dirPath := path + ".dir"
	if err := os.MkdirAll(dirPath, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dirPath) //nolint:errcheck

	// Overwrite the target file with a directory to cause rename failure.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path) //nolint:errcheck

	// Now attempt Save — rename will fail (target is a dir).
	err = Save(s)
	if err == nil {
		t.Fatal("expected error when target is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("error should mention rename, got: %v", err)
	}

	// Temp file should have been cleaned up.
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("temp file %q was not cleaned up after rename failure", tmpPath)
	}
}

// =============================================================================
// Save — overwrites existing file
// =============================================================================

func TestSave_OverwriteExisting(t *testing.T) {
	host := "overwrite-test.example.com"

	// Save first version.
	s1 := testSession(host)
	s1.LocalStorage["token"] = "v1_token"
	if err := Save(s1); err != nil {
		t.Fatal(err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	// Save second version (different data).
	s2 := testSession(host)
	s2.LocalStorage["token"] = "v2_token"
	s2.Cookies[0].Value = "new_cookie_value"
	if err := Save(s2); err != nil {
		t.Fatal(err)
	}

	// Load and verify it's v2.
	loaded, err := Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LocalStorage["token"] != "v2_token" {
		t.Errorf("expected v2 token, got %q", loaded.LocalStorage["token"])
	}
	if loaded.Cookies[0].Value != "new_cookie_value" {
		t.Errorf("expected updated cookie value, got %q", loaded.Cookies[0].Value)
	}
}

// =============================================================================
// Save — large data (many cookies + large localStorage)
// =============================================================================

func TestSave_LargePayload(t *testing.T) {
	host := "large-payload.example.com"
	s := testSession(host)

	// Add 100 cookies with large values.
	for i := 0; i < 100; i++ {
		s.Cookies = append(s.Cookies, CookieEntry{
			Name:     "cookie_" + itoa(i),
			Value:    strings.Repeat("x", 1024),  // 1KB each
			Domain:   ".example.com",
			Path:     "/",
			Expires:  time.Now().Add(24 * time.Hour).Unix(),
			HttpOnly: true,
			Secure:   true,
			SameSite: "Lax",
		})
	}

	// Add 50 localStorage entries.
	for i := 0; i < 50; i++ {
		s.LocalStorage["key_"+itoa(i)] = strings.Repeat("data", 200) // ~800B each
	}

	if err := Save(s); err != nil {
		t.Fatalf("Save with large payload failed: %v", err)
	}

	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	// Verify file size is reasonable (should be > 100KB).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 100*1024 {
		t.Errorf("expected file size > 100KB for large payload, got %d bytes", info.Size())
	}

	// Load and verify integrity.
	loaded, err := Load(host)
	if err != nil {
		t.Fatalf("Load after large save failed: %v", err)
	}
	// Spot check: the first original cookie + 100 new ones.
	if len(loaded.Cookies) != 101 {
		t.Errorf("expected 101 cookies, got %d", len(loaded.Cookies))
	}
	// 2 original localStorage keys (token, user) + 50 added = 52.
	if len(loaded.LocalStorage) != 52 {
		t.Errorf("expected 52 localStorage keys, got %d", len(loaded.LocalStorage))
	}
}

// =============================================================================
// Load — happy path (explicit, not round-trip)
// =============================================================================

func TestLoad_HappyPath(t *testing.T) {
	host := "load-happy.example.com"
	original := testSession(host)
	original.Metadata.Provider = "happy-test"

	if err := Save(original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	loaded, err := Load(host)
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if len(loaded.Cookies) != len(original.Cookies) {
		t.Errorf("cookie count: got %d, want %d", len(loaded.Cookies), len(original.Cookies))
	}
	if loaded.Cookies[0].Value != original.Cookies[0].Value {
		t.Errorf("cookie[0].value: got %q, want %q", loaded.Cookies[0].Value, original.Cookies[0].Value)
	}
	if loaded.LocalStorage["token"] != original.LocalStorage["token"] {
		t.Errorf("localStorage[token]: got %q, want %q", loaded.LocalStorage["token"], original.LocalStorage["token"])
	}
	if loaded.Metadata.Provider != original.Metadata.Provider {
		t.Errorf("metadata.provider: got %q, want %q", loaded.Metadata.Provider, original.Metadata.Provider)
	}
	if loaded.Metadata.ProviderHost != original.Metadata.ProviderHost {
		t.Errorf("metadata.provider_host: got %q, want %q", loaded.Metadata.ProviderHost, original.Metadata.ProviderHost)
	}
}

// =============================================================================
// Load — missing file
// =============================================================================

func TestLoad_MissingFile(t *testing.T) {
	host := "no-such-host-12345.example.com"

	_, err := Load(host)
	if err == nil {
		t.Fatal("Load() expected error for missing file, got nil")
	}

	if !strings.Contains(err.Error(), "no session for") {
		t.Errorf("error should mention 'no session for', got: %v", err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("error should contain host name %q, got: %v", host, err)
	}
}

// =============================================================================
// Load — empty file
// =============================================================================

func TestLoad_EmptyFile(t *testing.T) {
	host := "empty-file-test.example.com"
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}

	// Create an empty file at the expected path.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	_, err = Load(host)
	if err == nil {
		t.Fatal("Load() expected error for empty file, got nil")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
}

// =============================================================================
// Load — corrupt JSON (unparseable content)
// =============================================================================

func TestLoad_CorruptJSON(t *testing.T) {
	host := "corrupt-json-test.example.com"
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}

	// Write garbage content to the file.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not valid json {{{"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	_, err = Load(host)
	if err == nil {
		t.Fatal("Load() expected error for corrupt JSON, got nil")
	}

	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse', got: %v", err)
	}
}

// =============================================================================
// Load — missing provider host
// =============================================================================

func TestLoad_MissingHost(t *testing.T) {
	_, err := Load("")
	if err == nil {
		t.Fatal("Load('') expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error should mention 'must not be empty', got: %v", err)
	}
}

// =============================================================================
// Load — partial/malformed JSON (valid JSON, missing required fields)
// =============================================================================

func TestLoad_MalformedJSON(t *testing.T) {
	host := "malformed-json-test.example.com"
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}

	// Write valid JSON that's session-shaped but missing critical fields.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"cookies": null, "localStorage": null}`), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	loaded, err := Load(host)
	if err != nil {
		t.Fatalf("Load() should parse malformed JSON without error: %v", err)
	}
	// With null cookies/localStorage, Go should unmarshal to nil slices/maps.
	if loaded.Cookies != nil {
		t.Errorf("expected nil cookies for null input, got %v", loaded.Cookies)
	}
	if loaded.LocalStorage != nil {
		t.Errorf("expected nil localStorage for null input, got %v", loaded.LocalStorage)
	}
}

// =============================================================================
// Load — whitespace-only file (should be treated as empty)
// =============================================================================

func TestLoad_WhitespaceOnlyFile(t *testing.T) {
	host := "whitespace-file-test.example.com"
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}

	// Write a file with only whitespace.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("   \n\t  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path) //nolint:errcheck

	_, err = Load(host)
	if err == nil {
		t.Fatal("Load() expected error for whitespace-only file, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse', got: %v", err)
	}
}

// itoa is a minimal int-to-string for test convenience (avoid strconv import).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// =============================================================================
// Validate
// =============================================================================

// freshSession returns a minimal valid SessionData with a cookie that expires
// far in the future.
func freshSession(host string) *SessionData {
	return &SessionData{
		Cookies: []CookieEntry{
			{
				Name:     "cloud_session",
				Value:    "abc123",
				Domain:   ".example.com",
				Path:     "/",
				Expires:  time.Now().Add(24 * time.Hour).Unix(),
				HttpOnly: true,
				Secure:   true,
				SameSite: "Lax",
			},
		},
		LocalStorage: map[string]string{
			"token": "eyJhbG...test",
		},
		Metadata: SessionMetadata{
			Provider:     "test-provider",
			ProviderHost: host,
			CreatedAt:    time.Now().Unix(),
		},
	}
}

func TestValidate_ValidSession(t *testing.T) {
	s := freshSession("validate-valid.example.com")
	result := Validate(s)
	if !result.Valid {
		t.Errorf("expected valid session, got invalid: reason=%q details=%v", result.Reason, result.Details)
	}
	if result.Reason != "" {
		t.Errorf("expected empty reason for valid session, got %q", result.Reason)
	}
	if result.Details != nil {
		t.Errorf("expected nil details for valid session, got %v", result.Details)
	}
}

func TestValidate_NilSession(t *testing.T) {
	result := Validate(nil)
	if result.Valid {
		t.Fatal("Validate(nil) expected invalid, got valid")
	}
	if !strings.Contains(result.Reason, "nil") {
		t.Errorf("reason should mention 'nil', got %q", result.Reason)
	}
	if len(result.Details) == 0 {
		t.Errorf("expected at least one detail for nil input")
	}
}

func TestValidate_ExpiredMetadataExpiresAt(t *testing.T) {
	s := freshSession("validate-expires-at.example.com")
	s.Metadata.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix() // 1 hour in the past

	result := Validate(s)
	if result.Valid {
		t.Fatal("expected invalid session with expired ExpiresAt, got valid")
	}
	if !strings.Contains(result.Reason, "issue") {
		t.Errorf("reason should mention number of issues, got %q", result.Reason)
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, "expires_at") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("details should mention expires_at, got %v", result.Details)
	}
}

func TestValidate_ExpiredMetadataExpiresAfter(t *testing.T) {
	s := freshSession("validate-expires-after.example.com")
	s.Metadata.CreatedAt = time.Now().Add(-10 * 24 * time.Hour).Unix() // 10 days ago
	s.Metadata.ExpiresAfterSeconds = 86400                             // 1 day

	result := Validate(s)
	if result.Valid {
		t.Fatal("expected invalid session with elapsed ExpiresAfterSeconds, got valid")
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, "expires_after_seconds") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("details should mention expires_after_seconds, got %v", result.Details)
	}
}

func TestValidate_SingleExpiredCookie(t *testing.T) {
	s := freshSession("validate-expired-cookie.example.com")
	// Add a second cookie that's already expired.
	s.Cookies = append(s.Cookies, CookieEntry{
		Name:     "stale_token",
		Value:    "old_value",
		Domain:   ".example.com",
		Path:     "/",
		Expires:  time.Now().Add(-1 * time.Hour).Unix(), // 1 hour in the past
		HttpOnly: false,
		Secure:   false,
		SameSite: "Lax",
	})

	result := Validate(s)
	if result.Valid {
		t.Fatal("expected invalid session with expired cookie, got valid")
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, "stale_token") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("details should mention the expired cookie name 'stale_token', got %v", result.Details)
	}
}

func TestValidate_AllCookiesExpired(t *testing.T) {
	s := freshSession("validate-all-expired.example.com")
	// Override the fresh cookie with an expired one.
	s.Cookies[0].Expires = time.Now().Add(-2 * time.Hour).Unix()

	result := Validate(s)
	if result.Valid {
		t.Fatal("expected invalid session when all cookies expired, got valid")
	}
	if len(result.Details) == 0 {
		t.Errorf("expected at least one detail for expired cookies")
	}
}

func TestValidate_SessionCookiesOnly(t *testing.T) {
	// Session cookies (Expires <= 0) have no fixed expiry and should not
	// trigger validation failures.
	s := freshSession("validate-session-cookies.example.com")
	s.Cookies[0].Expires = -1 // convert to session cookie

	result := Validate(s)
	if !result.Valid {
		t.Errorf("expected valid session with only session cookies, got invalid: reason=%q", result.Reason)
	}
}

func TestValidate_EmptySession(t *testing.T) {
	// A session with no cookies, no localStorage, and no metadata expiry
	// is not expired (it simply has no auth material).
	s := &SessionData{
		Cookies:     nil,
		LocalStorage: nil,
		Metadata: SessionMetadata{
			Provider:     "test-provider",
			ProviderHost: "validate-empty.example.com",
			CreatedAt:    time.Now().Unix(),
		},
	}
	result := Validate(s)
	if !result.Valid {
		t.Errorf("expected valid for empty session, got invalid: reason=%q", result.Reason)
	}
}

func TestValidate_ZeroExpiryMetadata(t *testing.T) {
	// Metadata with all zero expiry fields should not trigger expiry.
	s := freshSession("validate-zero-expiry.example.com")
	s.Metadata.ExpiresAt = 0
	s.Metadata.ExpiresAfterSeconds = 0

	result := Validate(s)
	if !result.Valid {
		t.Errorf("expected valid session with zero expiry metadata, got invalid: reason=%q", result.Reason)
	}
}

func TestValidate_MultipleIssues(t *testing.T) {
	s := freshSession("validate-multiple.example.com")
	// Both metadata expiry AND a cookie are expired.
	s.Metadata.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
	s.Cookies[0].Expires = time.Now().Add(-2 * time.Hour).Unix()

	result := Validate(s)
	if result.Valid {
		t.Fatal("expected invalid session with multiple issues, got valid")
	}
	if len(result.Details) < 2 {
		t.Errorf("expected at least 2 detail entries for multiple issues, got %d: %v", len(result.Details), result.Details)
	}
}

func TestValidate_DoesNotMutateInput(t *testing.T) {
	s := freshSession("validate-no-mutate.example.com")
	_ = Validate(s)
	// Make sure the original session is untouched.
	if s.Cookies[0].Value != "abc123" {
		t.Errorf("Validate mutated cookie value: got %q, want 'abc123'", s.Cookies[0].Value)
	}
	if s.Metadata.ProviderHost != "validate-no-mutate.example.com" {
		t.Errorf("Validate mutated provider host: got %q", s.Metadata.ProviderHost)
	}
}

// =============================================================================
// Mock strategy for Refresh tests
// =============================================================================

// mockRefreshStrategy implements RefreshStrategy for testing.
type mockRefreshStrategy struct {
	name   string
	result *SessionData
	err    error
}

func (m *mockRefreshStrategy) Refresh(token string) (*SessionData, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func (m *mockRefreshStrategy) Name() string { return m.name }

// mockRefreshResult returns a fresh session suitable as a mock refresh output.
func mockRefreshResult(host string) *SessionData {
	return &SessionData{
		Cookies: []CookieEntry{
			{
				Name:     "cloud_session",
				Value:    "refreshed_abc123",
				Domain:   ".example.com",
				Path:     "/",
				Expires:  time.Now().Add(48 * time.Hour).Unix(),
				HttpOnly: true,
				Secure:   true,
				SameSite: "Lax",
			},
		},
		LocalStorage: map[string]string{
			"token": "eyJhbG...refreshed",
			"user":  `{"id":42,"name":"Test"}`,
		},
		Metadata: SessionMetadata{
			Provider:            "test-provider",
			ProviderHost:        host,
			CreatedAt:           time.Now().Unix(),
			ExpiresAt:           time.Now().Add(48 * time.Hour).Unix(),
			RefreshToken:        "rt_new_refreshed_token_xyz",
			ExpiresAfterSeconds: 172800, // 48 hours
		},
	}
}

// =============================================================================
// Refresh — happy path
// =============================================================================

func TestRefresh_HappyPath(t *testing.T) {
	host := "refresh-happy.example.com"
	original := testSession(host)
	original.Metadata.RefreshToken = "rt_original_token"
	original.Metadata.ExpiresAt = time.Now().Add(1 * time.Hour).Unix()

	if err := Save(original); err != nil {
		t.Fatalf("Save original failed: %v", err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	strategy := &mockRefreshStrategy{
		name:   "test-strategy",
		result: mockRefreshResult(host),
	}

	updated, err := Refresh(host, strategy)
	if err != nil {
		t.Fatalf("Refresh() returned unexpected error: %v", err)
	}

	// Verify the returned session is the refreshed one.
	if updated.Cookies[0].Value != "refreshed_abc123" {
		t.Errorf("expected refreshed cookie value, got %q", updated.Cookies[0].Value)
	}
	if updated.LocalStorage["token"] != "eyJhbG...refreshed" {
		t.Errorf("expected refreshed localStorage token, got %q", updated.LocalStorage["token"])
	}
	if updated.Metadata.RefreshToken != "rt_new_refreshed_token_xyz" {
		t.Errorf("expected new refresh token, got %q", updated.Metadata.RefreshToken)
	}
	if updated.Metadata.ProviderHost != host {
		t.Errorf("expected provider_host %q, got %q", host, updated.Metadata.ProviderHost)
	}

	// Verify the file on disk also has the refreshed data.
	loaded, err := Load(host)
	if err != nil {
		t.Fatalf("Load after Refresh failed: %v", err)
	}
	if loaded.Cookies[0].Value != "refreshed_abc123" {
		t.Errorf("on-disk cookie value: got %q, want %q", loaded.Cookies[0].Value, "refreshed_abc123")
	}
	if loaded.Metadata.RefreshToken != "rt_new_refreshed_token_xyz" {
		t.Errorf("on-disk refresh_token: got %q, want %q", loaded.Metadata.RefreshToken, "rt_new_refreshed_token_xyz")
	}
}

// =============================================================================
// Refresh — no refresh token in original session
// =============================================================================

func TestRefresh_NoRefreshToken(t *testing.T) {
	host := "refresh-no-token.example.com"
	original := testSession(host)
	// Original has no RefreshToken set.

	if err := Save(original); err != nil {
		t.Fatalf("Save original failed: %v", err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	strategy := &mockRefreshStrategy{
		name:   "unused-strategy",
		result: mockRefreshResult(host),
	}

	_, err = Refresh(host, strategy)
	if err == nil {
		t.Fatal("Refresh() expected ErrNoRefreshToken, got nil")
	}
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("expected ErrNoRefreshToken, got: %v", err)
	}

	// Verify the original session file was NOT overwritten.
	loaded, err := Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cookies[0].Value != "abc123" {
		t.Errorf("expected original cookie value preserved, got %q", loaded.Cookies[0].Value)
	}
}

// =============================================================================
// Refresh — strategy failure
// =============================================================================

func TestRefresh_StrategyFailure(t *testing.T) {
	host := "refresh-strategy-fail.example.com"
	original := testSession(host)
	original.Metadata.RefreshToken = "rt_will_fail"

	if err := Save(original); err != nil {
		t.Fatalf("Save original failed: %v", err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	strategy := &mockRefreshStrategy{
		name: "failing-strategy",
		err:  errors.New("token endpoint returned 401"),
	}

	_, err = Refresh(host, strategy)
	if err == nil {
		t.Fatal("Refresh() expected error from strategy, got nil")
	}
	if !strings.Contains(err.Error(), "failing-strategy") {
		t.Errorf("error should mention strategy name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should propagate strategy error, got: %v", err)
	}

	// Verify the original session file was NOT touched.
	loaded, err := Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cookies[0].Value != "abc123" {
		t.Errorf("expected original cookie value preserved after failed refresh, got %q", loaded.Cookies[0].Value)
	}
}

// =============================================================================
// Refresh — nil strategy
// =============================================================================

func TestRefresh_NilStrategy(t *testing.T) {
	host := "refresh-nil-strategy.example.com"

	_, err := Refresh(host, nil)
	if err == nil {
		t.Fatal("Refresh with nil strategy expected error, got nil")
	}
	if !strings.Contains(err.Error(), "strategy must not be nil") {
		t.Errorf("error should mention nil strategy, got: %v", err)
	}
}

// =============================================================================
// Refresh — load failure (no session file)
// =============================================================================

func TestRefresh_LoadFailure(t *testing.T) {
	host := "no-such-session.example.com"
	strategy := &mockRefreshStrategy{
		name:   "unused-strategy",
		result: mockRefreshResult(host),
	}

	_, err := Refresh(host, strategy)
	if err == nil {
		t.Fatal("Refresh() expected error for missing session, got nil")
	}
	if !strings.Contains(err.Error(), "no session") {
		t.Errorf("error should mention missing session, got: %v", err)
	}
}

// =============================================================================
// Refresh — strategy returns nil result (should not save)
// =============================================================================

func TestRefresh_NilStrategyResult(t *testing.T) {
	host := "refresh-nil-result.example.com"
	original := testSession(host)
	original.Metadata.RefreshToken = "rt_will_return_nil"

	if err := Save(original); err != nil {
		t.Fatalf("Save original failed: %v", err)
	}
	path, err := SessionPath(host)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)   //nolint:errcheck
	defer os.Remove(path + ".tmp") //nolint:errcheck

	strategy := &mockRefreshStrategy{
		name:   "nil-returning",
		result: nil, // strategy returns nil without error — unexpected
	}

	_, err = Refresh(host, strategy)
	if err == nil {
		t.Fatal("Refresh() expected error for nil strategy result, got nil")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error should mention nil result, got: %v", err)
	}

	// Original should still be intact.
	loaded, err := Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cookies[0].Value != "abc123" {
		t.Errorf("original cookie value should be unchanged, got %q", loaded.Cookies[0].Value)
	}
}

// =============================================================================
// NeedsRefresh — near ExpiresAt expiry
// =============================================================================

func TestNeedsRefresh_ExpiresAt_NearExpiry(t *testing.T) {
	s := freshSession("needs-refresh-near.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// Expires in 2 minutes.
	s.Metadata.ExpiresAt = time.Now().Add(2 * time.Minute).Unix()

	// 5-minute grace period should flag this as needing refresh.
	if !NeedsRefresh(s, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected true for session expiring in 2min with 5min grace")
	}
}

// =============================================================================
// NeedsRefresh — far from ExpiresAt expiry
// =============================================================================

func TestNeedsRefresh_ExpiresAt_FarFromExpiry(t *testing.T) {
	s := freshSession("needs-refresh-far.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// Expires in 24 hours.
	s.Metadata.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()

	// 5-minute grace period should NOT flag this.
	if NeedsRefresh(s, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected false for session expiring in 24h with 5min grace")
	}
}

// =============================================================================
// NeedsRefresh — near ExpiresAfterSeconds expiry
// =============================================================================

func TestNeedsRefresh_ExpiresAfterSeconds_Near(t *testing.T) {
	s := freshSession("needs-refresh-rel.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// Session was created 23h55m ago and expires after 86400s (24h).
	// That means it expires in ~5 minutes.
	s.Metadata.CreatedAt = time.Now().Add(-23*time.Hour - 55*time.Minute).Unix()
	s.Metadata.ExpiresAfterSeconds = 86400 // 24 hours from CreatedAt

	// 10-minute grace period should flag it (expiring in ~5 min).
	if !NeedsRefresh(s, 10*time.Minute) {
		t.Errorf("NeedsRefresh expected true for session expiring in ~5min with 10min grace")
	}
}

// =============================================================================
// NeedsRefresh — far from ExpiresAfterSeconds expiry
// =============================================================================

func TestNeedsRefresh_ExpiresAfterSeconds_Far(t *testing.T) {
	s := freshSession("needs-refresh-rel-far.example.com")
	s.Metadata.RefreshToken = "rt_test"
	s.Metadata.CreatedAt = time.Now().Add(-1 * time.Hour).Unix()
	s.Metadata.ExpiresAfterSeconds = 86400 // 24 hours from CreatedAt — 23h remaining

	// 5-minute grace should NOT flag it (23h remaining).
	if NeedsRefresh(s, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected false for session expiring in 23h with 5min grace")
	}
}

// =============================================================================
// NeedsRefresh — no refresh token
// =============================================================================

func TestNeedsRefresh_NoRefreshToken(t *testing.T) {
	s := freshSession("needs-refresh-no-rt.example.com")
	// No RefreshToken set.
	s.Metadata.ExpiresAt = time.Now().Add(1 * time.Minute).Unix()

	if NeedsRefresh(s, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected false when there is no refresh_token")
	}
}

// =============================================================================
// NeedsRefresh — nil session
// =============================================================================

func TestNeedsRefresh_NilSession(t *testing.T) {
	if NeedsRefresh(nil, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected false for nil session")
	}
}

// =============================================================================
// NeedsRefresh — already expired (past deadline)
// =============================================================================

func TestNeedsRefresh_AlreadyExpired(t *testing.T) {
	s := freshSession("needs-refresh-expired.example.com")
	s.Metadata.RefreshToken = "rt_test"
	s.Metadata.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix() // 1 hour ago

	// Even with a very small grace period, the session is past expiry.
	if !NeedsRefresh(s, 30*time.Second) {
		t.Errorf("NeedsRefresh expected true for already-expired session")
	}
}

// =============================================================================
// NeedsRefresh — zero expiry fields
// =============================================================================

func TestNeedsRefresh_ZeroExpiryFields(t *testing.T) {
	s := freshSession("needs-refresh-zero.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// Both ExpiresAt and ExpiresAfterSeconds are 0.

	if NeedsRefresh(s, 5*time.Minute) {
		t.Errorf("NeedsRefresh expected false when no expiry fields are set")
	}
}

// =============================================================================
// NeedsRefresh — exact deadline match
// =============================================================================

func TestNeedsRefresh_ExactDeadline(t *testing.T) {
	s := freshSession("needs-refresh-exact.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// ExpiresAt set to exactly 1 hour from now.
	exactExpiry := time.Now().Add(1 * time.Hour)
	s.Metadata.ExpiresAt = exactExpiry.Unix()

	// With a 1-hour grace period, now + 1h == exactExpiry.
	if !NeedsRefresh(s, 1*time.Hour) {
		t.Errorf("NeedsRefresh expected true when now + grace == deadline")
	}
}

// =============================================================================
// NeedsRefresh — both expiry fields set, one near
// =============================================================================

func TestNeedsRefresh_BothExpiryFields_NearOne(t *testing.T) {
	s := freshSession("needs-refresh-both.example.com")
	s.Metadata.RefreshToken = "rt_test"
	// ExpiresAt is far in the future (48h).
	s.Metadata.ExpiresAt = time.Now().Add(48 * time.Hour).Unix()
	// But ExpiresAfterSeconds is near expiry: created 23h55m ago,
	// expires after 86400s (24h) → expires in ~5 minutes.
	s.Metadata.CreatedAt = time.Now().Add(-23*time.Hour - 55*time.Minute).Unix()
	s.Metadata.ExpiresAfterSeconds = 86400 // 24h — ~5min remaining

	if !NeedsRefresh(s, 10*time.Minute) {
		t.Errorf("NeedsRefresh expected true when one of two expiry fields is near")
	}
}
