package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

// =============================================================================
// Helpers
// =============================================================================

// testCookies returns a slice of rod proto.NetworkCookie suitable for testing.
func testCookies() []*proto.NetworkCookie {
	return []*proto.NetworkCookie{
		{
			Name:     "cloud_session",
			Value:    "abc123",
			Domain:   ".example.com",
			Path:     "/",
			Expires:  proto.TimeSinceEpoch(1760731860),
			Size:     6,
			HTTPOnly: true,
			Secure:   true,
			SameSite: proto.NetworkCookieSameSiteLax,
		},
	}
}

// testLocalStorage returns a representative localStorage map.
func testLocalStorage() map[string]string {
	return map[string]string{
		"token": "eyJhbGciOiJIUzI1NiJ9.test",
		"user":  `{"id":42,"name":"Test User"}`,
	}
}

// =============================================================================
// Save — happy path
// =============================================================================

func TestFileStore_Save_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deepseek_session.json")
	s := NewFileStore(path)

	cookies := testCookies()
	ls := testLocalStorage()

	if err := s.Save(cookies, ls); err != nil {
		t.Fatalf("Save() returned unexpected error: %v", err)
	}

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session file not found at %q: %v", path, err)
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("session file contains invalid JSON: %v", err)
	}

	// Spot-check fields.
	if len(sf.Cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(sf.Cookies))
	}
	if sf.Cookies[0].Name != "cloud_session" {
		t.Errorf("expected cookie name 'cloud_session', got %q", sf.Cookies[0].Name)
	}
	if sf.Cookies[0].Value != "abc123" {
		t.Errorf("expected cookie value 'abc123', got %q", sf.Cookies[0].Value)
	}
	if sf.Cookies[0].Domain != ".example.com" {
		t.Errorf("expected domain '.example.com', got %q", sf.Cookies[0].Domain)
	}
	if sf.Cookies[0].HTTPOnly != true {
		t.Errorf("expected httpOnly=true, got %v", sf.Cookies[0].HTTPOnly)
	}
	if sf.Cookies[0].Secure != true {
		t.Errorf("expected secure=true, got %v", sf.Cookies[0].Secure)
	}
	if sf.Cookies[0].SameSite != "Lax" {
		t.Errorf("expected sameSite='Lax', got %q", sf.Cookies[0].SameSite)
	}
	if sf.LocalStorage["token"] != "eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("localStorage token mismatch")
	}
	if sf.LocalStorage["user"] != `{"id":42,"name":"Test User"}` {
		t.Errorf("localStorage user mismatch")
	}
}

// =============================================================================
// Save — empty cookie list
// =============================================================================

func TestFileStore_Save_EmptyCookies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty_cookies.json")
	s := NewFileStore(path)

	if err := s.Save([]*proto.NetworkCookie{}, testLocalStorage()); err != nil {
		t.Fatalf("Save() with empty cookies returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not found: %v", err)
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(sf.Cookies) != 0 {
		t.Errorf("expected 0 cookies, got %d", len(sf.Cookies))
	}
	if sf.LocalStorage["token"] != "eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("localStorage should still be present")
	}
}

// =============================================================================
// Save — nil localStorage
// =============================================================================

func TestFileStore_Save_NilLocalStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nil_ls.json")
	s := NewFileStore(path)

	if err := s.Save(testCookies(), nil); err != nil {
		t.Fatalf("Save() with nil localStorage returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not found: %v", err)
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if sf.LocalStorage == nil {
		t.Error("localStorage in file should not be nil (should be empty map)")
	}
	if len(sf.LocalStorage) != 0 {
		t.Errorf("expected empty localStorage, got %d keys", len(sf.LocalStorage))
	}
}

// =============================================================================
// Save — empty path
// =============================================================================

func TestFileStore_Save_EmptyPath(t *testing.T) {
	s := NewFileStore("")
	err := s.Save(testCookies(), testLocalStorage())
	if err == nil {
		t.Fatal("Save() with empty path expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Errorf("error should mention 'empty path', got: %v", err)
	}
}

// =============================================================================
// Save — atomic write (temp file cleaned up)
// =============================================================================

func TestFileStore_Save_AtomicWrite_CleanTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.json")
	s := NewFileStore(path)

	if err := s.Save(testCookies(), testLocalStorage()); err != nil {
		t.Fatal(err)
	}

	// Temp file should NOT exist after successful save.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Errorf("temp file %q still exists after successful Save — atomic write leak", path+".tmp")
	}
}

// =============================================================================
// Save — multiple cookies
// =============================================================================

func TestFileStore_Save_MultipleCookies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi_cookies.json")
	s := NewFileStore(path)

	cookies := []*proto.NetworkCookie{
		{Name: "session", Value: "abc", Domain: ".example.com", Path: "/"},
		{Name: "prefs", Value: "dark_mode", Domain: ".example.com", Path: "/"},
		{Name: "csrf", Value: "tok_xyz", Domain: ".example.com", Path: "/", HTTPOnly: true},
	}
	ls := map[string]string{"key": "val"}

	if err := s.Save(cookies, ls); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not found: %v", err)
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(sf.Cookies) != 3 {
		t.Fatalf("expected 3 cookies, got %d", len(sf.Cookies))
	}
	if sf.Cookies[0].Name != "session" {
		t.Errorf("cookie[0].name: got %q", sf.Cookies[0].Name)
	}
	if sf.Cookies[2].Name != "csrf" {
		t.Errorf("cookie[2].name: got %q", sf.Cookies[2].Name)
	}
	if sf.Cookies[2].HTTPOnly != true {
		t.Errorf("cookie[2].httpOnly should be true")
	}
}

// =============================================================================
// Save — creates parent directories
// =============================================================================

func TestFileStore_Save_CreatesParentDir(t *testing.T) {
	baseDir := t.TempDir()
	// Use a deeply nested path that does not exist.
	path := filepath.Join(baseDir, "a", "b", "c", "deepseek_session.json")
	s := NewFileStore(path)

	if err := s.Save(testCookies(), testLocalStorage()); err != nil {
		t.Fatalf("Save() with new nested dirs returned error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist after save: %v", err)
	}
}

// =============================================================================
// Load — happy path (explicit, not round-trip)
// =============================================================================

func TestFileStore_Load_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deepseek_session.json")
	s := NewFileStore(path)

	// Save first.
	cookies := testCookies()
	ls := testLocalStorage()
	if err := s.Save(cookies, ls); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load.
	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if len(gotCookies) != len(cookies) {
		t.Fatalf("cookie count: got %d, want %d", len(gotCookies), len(cookies))
	}
	if gotCookies[0].Name != cookies[0].Name {
		t.Errorf("cookie[0].name: got %q, want %q", gotCookies[0].Name, cookies[0].Name)
	}
	if gotCookies[0].Value != cookies[0].Value {
		t.Errorf("cookie[0].value: got %q, want %q", gotCookies[0].Value, cookies[0].Value)
	}
	if gotCookies[0].Domain != cookies[0].Domain {
		t.Errorf("cookie[0].domain: got %q, want %q", gotCookies[0].Domain, cookies[0].Domain)
	}
	if gotCookies[0].HTTPOnly != cookies[0].HTTPOnly {
		t.Errorf("cookie[0].httpOnly: got %v, want %v", gotCookies[0].HTTPOnly, cookies[0].HTTPOnly)
	}
	if gotCookies[0].Secure != cookies[0].Secure {
		t.Errorf("cookie[0].secure: got %v, want %v", gotCookies[0].Secure, cookies[0].Secure)
	}

	for k, v := range ls {
		if gotLS[k] != v {
			t.Errorf("localStorage[%q]: got %q, want %q", k, gotLS[k], v)
		}
	}
}

// =============================================================================
// Load — missing file returns zero values
// =============================================================================

func TestFileStore_Load_MissingFile(t *testing.T) {
	dir := t.TempDir()
	// File that does not exist.
	path := filepath.Join(dir, "nonexistent.json")
	s := NewFileStore(path)

	cookies, ls, err := s.Load()
	if err != nil {
		t.Fatalf("Load() on missing file should return nil error, got: %v", err)
	}
	if cookies != nil {
		t.Errorf("expected nil cookies for missing file, got %d entries", len(cookies))
	}
	if ls != nil {
		t.Errorf("expected nil localStorage for missing file, got %d entries", len(ls))
	}
}

// =============================================================================
// Load — empty file returns zero values
// =============================================================================

func TestFileStore_Load_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	s := NewFileStore(path)

	// Create empty file.
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	cookies, ls, err := s.Load()
	if err != nil {
		t.Fatalf("Load() on empty file should return nil error, got: %v", err)
	}
	if cookies != nil {
		t.Errorf("expected nil cookies for empty file, got %d entries", len(cookies))
	}
	if ls != nil {
		t.Errorf("expected nil localStorage for empty file, got %d entries", len(ls))
	}
}

// =============================================================================
// Load — corrupt JSON
// =============================================================================

func TestFileStore_Load_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	s := NewFileStore(path)

	if err := os.WriteFile(path, []byte("{invalid json}"), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.Load()
	if err == nil {
		t.Fatal("Load() on corrupt file expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse', got: %v", err)
	}
}

// =============================================================================
// Load — empty path
// =============================================================================

func TestFileStore_Load_EmptyPath(t *testing.T) {
	s := NewFileStore("")
	_, _, err := s.Load()
	if err == nil {
		t.Fatal("Load() with empty path expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Errorf("error should mention 'empty path', got: %v", err)
	}
}

// =============================================================================
// Save + Load round-trip
// =============================================================================

func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.json")
	s := NewFileStore(path)

	cookies := []*proto.NetworkCookie{
		{
			Name:     "cloud_session",
			Value:    "abc123",
			Domain:   ".deepseek.com",
			Path:     "/",
			Expires:  proto.TimeSinceEpoch(1760731860),
			Size:     6,
			HTTPOnly: true,
			Secure:   true,
			SameSite: proto.NetworkCookieSameSiteLax,
		},
		{
			Name:     "refresh_token",
			Value:    "rt_xyz789",
			Domain:   ".deepseek.com",
			Path:     "/api",
			Expires:  proto.TimeSinceEpoch(1760731860),
			Size:     10,
			HTTPOnly: true,
			Secure:   true,
			SameSite: proto.NetworkCookieSameSiteStrict,
		},
	}
	ls := map[string]string{
		"token":       "eyJhbGciOiJIUzI1NiJ9.test",
		"user":        `{"id":42,"name":"Test"}`,
		"theme":       "dark",
		"lang":        "en",
	}

	if err := s.Save(cookies, ls); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Cookie count.
	if len(gotCookies) != len(cookies) {
		t.Fatalf("cookie count: got %d, want %d", len(gotCookies), len(cookies))
	}

	// Cookie field equality.
	for i, want := range cookies {
		got := gotCookies[i]
		if got.Name != want.Name {
			t.Errorf("cookie[%d].name: got %q, want %q", i, got.Name, want.Name)
		}
		if got.Value != want.Value {
			t.Errorf("cookie[%d].value: got %q, want %q", i, got.Value, want.Value)
		}
		if got.Domain != want.Domain {
			t.Errorf("cookie[%d].domain: got %q, want %q", i, got.Domain, want.Domain)
		}
		if got.Path != want.Path {
			t.Errorf("cookie[%d].path: got %q, want %q", i, got.Path, want.Path)
		}
		if got.HTTPOnly != want.HTTPOnly {
			t.Errorf("cookie[%d].httpOnly: got %v, want %v", i, got.HTTPOnly, want.HTTPOnly)
		}
		if got.Secure != want.Secure {
			t.Errorf("cookie[%d].secure: got %v, want %v", i, got.Secure, want.Secure)
		}
		if got.SameSite != want.SameSite {
			t.Errorf("cookie[%d].sameSite: got %q, want %q", i, got.SameSite, want.SameSite)
		}
	}

	// localStorage field equality.
	if len(gotLS) != len(ls) {
		t.Errorf("localStorage key count: got %d, want %d", len(gotLS), len(ls))
	}
	for k, v := range ls {
		if gotLS[k] != v {
			t.Errorf("localStorage[%q]: got %q, want %q", k, gotLS[k], v)
		}
	}
}

// =============================================================================
// Save — overwrites existing file with new data
// =============================================================================

func TestFileStore_Save_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.json")
	s := NewFileStore(path)

	// Save v1.
	v1Cookies := []*proto.NetworkCookie{
		{Name: "session", Value: "v1_val", Domain: ".a.com", Path: "/"},
	}
	v1LS := map[string]string{"token": "v1"}
	if err := s.Save(v1Cookies, v1LS); err != nil {
		t.Fatal(err)
	}

	// Save v2 (different data).
	v2Cookies := []*proto.NetworkCookie{
		{Name: "session", Value: "v2_val", Domain: ".b.com", Path: "/"},
	}
	v2LS := map[string]string{"token": "v2"}
	if err := s.Save(v2Cookies, v2LS); err != nil {
		t.Fatal(err)
	}

	// Load and verify it's v2.
	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCookies) != 1 || gotCookies[0].Value != "v2_val" {
		t.Errorf("expected cookie value 'v2_val', got %q", gotCookies[0].Value)
	}
	if gotLS["token"] != "v2" {
		t.Errorf("expected token 'v2', got %q", gotLS["token"])
	}
}

// =============================================================================
// Large payload (many cookies + many localStorage keys)
// =============================================================================

func TestFileStore_LargePayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.json")
	s := NewFileStore(path)

	// 100 cookies with 1KB values each.
	cookies := make([]*proto.NetworkCookie, 100)
	for i := 0; i < 100; i++ {
		cookies[i] = &proto.NetworkCookie{
			Name:     "cookie_" + itoa(i),
			Value:    strings.Repeat("x", 1024),
			Domain:   ".example.com",
			Path:     "/",
			HTTPOnly: true,
			Secure:   true,
			SameSite: proto.NetworkCookieSameSiteLax,
		}
	}

	// 50 localStorage entries with ~800B values each.
	ls := make(map[string]string, 50)
	for i := 0; i < 50; i++ {
		ls["key_"+itoa(i)] = strings.Repeat("data", 200)
	}

	if err := s.Save(cookies, ls); err != nil {
		t.Fatalf("Save with large payload failed: %v", err)
	}

	// Verify file size is reasonable (> 100KB expected).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 100*1024 {
		t.Errorf("expected file size > 100KB for large payload, got %d bytes", info.Size())
	}

	// Load and verify integrity.
	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatalf("Load after large save failed: %v", err)
	}
	if len(gotCookies) != 100 {
		t.Errorf("expected 100 cookies, got %d", len(gotCookies))
	}
	if len(gotLS) != 50 {
		t.Errorf("expected 50 localStorage keys, got %d", len(gotLS))
	}
	if gotCookies[99].Name != "cookie_99" {
		t.Errorf("cookie[99].name: got %q", gotCookies[99].Name)
	}
	if gotCookies[50].Value != strings.Repeat("x", 1024) {
		t.Errorf("cookie[50].value corrupted after round-trip")
	}
}

// =============================================================================
// Load after Save with empty cookies sees empty slice in file
// =============================================================================

func TestFileStore_Load_EmptyCookies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty_cookies_rt.json")
	s := NewFileStore(path)

	if err := s.Save([]*proto.NetworkCookie{}, testLocalStorage()); err != nil {
		t.Fatal(err)
	}

	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCookies) != 0 {
		t.Errorf("expected 0 cookies, got %d", len(gotCookies))
	}
	if gotLS["token"] != "eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("localStorage broken after empty-cookie round-trip")
	}
}

// =============================================================================
// Load after Save with nil localStorage sees empty map in file
// =============================================================================

func TestFileStore_Load_NilLocalStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nil_ls_rt.json")
	s := NewFileStore(path)

	if err := s.Save(testCookies(), nil); err != nil {
		t.Fatal(err)
	}

	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCookies) != 1 {
		t.Errorf("expected 1 cookie, got %d", len(gotCookies))
	}
	if len(gotLS) != 0 {
		t.Errorf("expected 0 localStorage keys, got %d", len(gotLS))
	}
}

// =============================================================================
// Concurrent save/load safety — write-then-read in sequence
// =============================================================================

func TestFileStore_WriteThenReadSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seq.json")
	s := NewFileStore(path)

	// Write 10 versions, read after each.
	for i := 0; i < 10; i++ {
		cookies := []*proto.NetworkCookie{
			{
				Name:  "session",
				Value: "val_" + itoa(i),
				Domain: ".example.com",
				Path:  "/",
			},
		}
		ls := map[string]string{"version": itoa(i)}
		if err := s.Save(cookies, ls); err != nil {
			t.Fatalf("Save iteration %d failed: %v", i, err)
		}

		gotCookies, gotLS, err := s.Load()
		if err != nil {
			t.Fatalf("Load iteration %d failed: %v", i, err)
		}
		if len(gotCookies) != 1 || gotCookies[0].Value != "val_"+itoa(i) {
			t.Errorf("iteration %d: bad cookie value", i)
		}
		if gotLS["version"] != itoa(i) {
			t.Errorf("iteration %d: bad version", i)
		}
	}
}

// =============================================================================
// Path with special characters
// =============================================================================

func TestFileStore_PathWithSpecialChars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "special chars+(123).json")
	s := NewFileStore(path)

	cookies := testCookies()
	if err := s.Save(cookies, testLocalStorage()); err != nil {
		t.Fatalf("Save with special chars in path failed: %v", err)
	}

	gotCookies, gotLS, err := s.Load()
	if err != nil {
		t.Fatalf("Load with special chars in path failed: %v", err)
	}
	if len(gotCookies) != 1 {
		t.Errorf("expected 1 cookie, got %d", len(gotCookies))
	}
	if gotLS["token"] != "eyJhbGciOiJIUzI1NiJ9.test" {
		t.Errorf("localStorage mismatch")
	}
}

// =============================================================================
// Interface compliance (compile-time check)
// =============================================================================

var _ SessionStore = (*FileStore)(nil)
