package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-rod/rod/lib/proto"
)

// =============================================================================
// SessionStore — interface for persisting / restoring browser session state
// =============================================================================

// SessionStore persists and restores the cookies and localStorage that a
// go-rod browser context accumulated during an authenticated login.
//
// Implementations must be safe for the provider to call at any point in its
// lifecycle — Save after a successful login, Load before creating a browser
// context, Load also to check whether a saved session exists.
type SessionStore interface {
	// Save stores the browser's cookies and localStorage to durable storage.
	// Both parameters come directly from rod's Browser.GetCookies and
	// Page.Eval("JSON.stringify(localStorage)").
	Save(cookies []*proto.NetworkCookie, localStorage map[string]string) error

	// Load retrieves a previously saved session.
	//
	// When no saved session exists (file not found), Load returns zero values
	// (nil slices/maps, nil error) so the caller can proceed with a fresh
	// login flow without special-casing the missing-file case.
	Load() (cookies []*proto.NetworkCookie, localStorage map[string]string, err error)
}

// =============================================================================
// storeFile — JSON envelope for a serialised session
// =============================================================================

// storeFile is the on-disk JSON structure written by FileStore.
// We use a dedicated envelope rather than marshalling proto.NetworkCookie
// directly so that the file format is stable across rod version bumps and
// we don't leak proto-internal fields into the serialised representation.
type storeFile struct {
	Cookies      []cookieJSON      `json:"cookies"`
	LocalStorage map[string]string `json:"localStorage"`
}

// cookieJSON is the serialisable subset of proto.NetworkCookie fields that
// are needed to restore a browser session via SetCookies.
type cookieJSON struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}

// =============================================================================
// FileStore — JSON-file-backed SessionStore
// =============================================================================

// FileStore is a SessionStore that persists session data as a single JSON
// file on the local filesystem.
//
// Usage
//
//	s := session.NewFileStore("/path/to/deepseek_session.json")
//	if err := s.Save(cookies, localStorage); err != nil { ... }
//	cookies, ls, err := s.Load()
//
// The file is written atomically (temp file → rename) to prevent partial
// reads from concurrent or crashed writers.
type FileStore struct {
	// path is the absolute (or relative-to-cwd) path of the session file.
	path string
}

// NewFileStore creates a FileStore that reads and writes the session file
// at the given path.
//
// The parent directory is created on Save if it does not exist. Load
// returns zero values when the file does not exist (it is not an error).
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Save implements SessionStore.Save.
func (s *FileStore) Save(cookies []*proto.NetworkCookie, localStorage map[string]string) error {
	if s.path == "" {
		return fmt.Errorf("session store: empty path")
	}

	// Convert rod cookies to our portable JSON envelope.
	store := storeFile{
		Cookies:      make([]cookieJSON, len(cookies)),
		LocalStorage: localStorage,
	}
	if store.LocalStorage == nil {
		store.LocalStorage = map[string]string{}
	}
	for i, c := range cookies {
		store.Cookies[i] = cookieJSON{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  float64(c.Expires),
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: string(c.SameSite),
		}
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("session store: marshal %q: %w", s.path, err)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("session store: create dir %q: %w", dir, err)
	}

	// Atomic write.
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("session store: write temp %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("session store: rename %q → %q: %w", tmpPath, s.path, err)
	}

	return nil
}

// Load implements SessionStore.Load.
//
// Returns zero values (nil slices, nil maps, nil error) when the file does
// not exist — the caller can safely use the result to detect "no saved
// session" and proceed with a fresh login.
func (s *FileStore) Load() (cookies []*proto.NetworkCookie, localStorage map[string]string, err error) {
	if s.path == "" {
		return nil, nil, fmt.Errorf("session store: empty path")
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// No saved session — zero values so caller proceeds cleanly.
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("session store: read %q: %w", s.path, err)
	}

	// Empty file is treated the same as missing — nothing to restore.
	if len(data) == 0 {
		return nil, nil, nil
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, nil, fmt.Errorf("session store: parse %q: %w", s.path, err)
	}

	// Convert portable cookies back to rod proto.NetworkCookie.
	result := make([]*proto.NetworkCookie, len(sf.Cookies))
	for i, cj := range sf.Cookies {
		result[i] = &proto.NetworkCookie{
			Name:     cj.Name,
			Value:    cj.Value,
			Domain:   cj.Domain,
			Path:     cj.Path,
			Expires:  proto.TimeSinceEpoch(cj.Expires),
			Size:     len(cj.Value),
			HTTPOnly: cj.HTTPOnly,
			Secure:   cj.Secure,
			SameSite: proto.NetworkCookieSameSite(cj.SameSite),
		}
	}

	lcl := sf.LocalStorage
	if lcl == nil {
		lcl = map[string]string{}
	}

	return result, lcl, nil
}
