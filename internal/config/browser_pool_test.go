package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boolIs(b *bool) bool {
	return b != nil && *b
}

func TestDefaultBrowserPoolConfig(t *testing.T) {
	cfg := DefaultBrowserPoolConfig()

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected heartbeat 5m, got %v", cfg.HeartbeatInterval)
	}
	// Should have both default providers
	if _, ok := cfg.Providers["deepseek"]; !ok {
		t.Error("expected deepseek provider in defaults")
	}
	if _, ok := cfg.Providers["mimo"]; !ok {
		t.Error("expected mimo provider in defaults")
	}
	// Each should default to 1 slot
	for name, p := range cfg.Providers {
		if p.Slots != 1 {
			t.Errorf("provider %q: expected 1 slot by default, got %d", name, p.Slots)
		}
	}
	// Default launch opts
	if cfg.DefaultLaunchOpts == nil {
		t.Fatal("expected non-nil DefaultLaunchOpts")
	}
	if !boolIs(cfg.DefaultLaunchOpts.Headless) {
		t.Error("expected Headless=true by default")
	}
	if cfg.DefaultLaunchOpts.WindowSize != "1920x1080" {
		t.Errorf("expected WindowSize 1920x1080, got %q", cfg.DefaultLaunchOpts.WindowSize)
	}
}

func TestLoadBrowserPoolConfig_DefaultsOnly(t *testing.T) {
	// No config file exists — should return defaults
	cfg, err := LoadBrowserPoolConfig("/nonexistent/path.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected 5m heartbeat, got %v", cfg.HeartbeatInterval)
	}
	if len(cfg.Providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(cfg.Providers))
	}
}

func TestLoadBrowserPoolConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "browser_pool.yaml")
	yamlContent := `
heartbeat_interval: 2m
providers:
  deepseek:
    slots: 3
  mimo:
    slots: 2
default_launch_opts:
  headless: false
  window_size: "1280x720"
  proxy: "http://proxy.example.com:3128"
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(yamlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HeartbeatInterval != 2*time.Minute {
		t.Errorf("expected 2m heartbeat, got %v", cfg.HeartbeatInterval)
	}

	if cfg.Providers["deepseek"].Slots != 3 {
		t.Errorf("deepseek slots: expected 3, got %d", cfg.Providers["deepseek"].Slots)
	}
	if cfg.Providers["mimo"].Slots != 2 {
		t.Errorf("mimo slots: expected 2, got %d", cfg.Providers["mimo"].Slots)
	}

	// Launch opts merged from file overrides
	if boolIs(cfg.DefaultLaunchOpts.Headless) {
		t.Error("expected Headless=false (overridden)")
	}
	if cfg.DefaultLaunchOpts.WindowSize != "1280x720" {
		t.Errorf("expected WindowSize 1280x720, got %q", cfg.DefaultLaunchOpts.WindowSize)
	}
	if cfg.DefaultLaunchOpts.Proxy != "http://proxy.example.com:3128" {
		t.Errorf("expected proxy override, got %q", cfg.DefaultLaunchOpts.Proxy)
	}
	// ExtraArgs should fall back to default (empty slice)
	if cfg.DefaultLaunchOpts.ExtraArgs == nil {
		t.Error("expected non-nil ExtraArgs (default)")
	}

	// Per-provider launch opts should inherit headless:false from defaults
	for name, p := range cfg.Providers {
		if p.LaunchOpts == nil {
			t.Errorf("provider %q has nil LaunchOpts", name)
		} else if boolIs(p.LaunchOpts.Headless) {
			t.Errorf("provider %q LaunchOpts.Headless should be false (inherited from defaults)", name)
		}
	}
}

func TestLoadBrowserPoolConfig_PartialOverride(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "partial.yaml")
	yamlContent := `
providers:
  deepseek:
    slots: 5
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(yamlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// deepseek should be overridden
	if cfg.Providers["deepseek"].Slots != 5 {
		t.Errorf("deepseek slots: expected 5, got %d", cfg.Providers["deepseek"].Slots)
	}
	// mimo should still be present with defaults
	if cfg.Providers["mimo"].Slots != 1 {
		t.Errorf("mimo slots: expected 1 (default), got %d", cfg.Providers["mimo"].Slots)
	}
	// heartbeat defaults to 5m
	if cfg.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected 5m heartbeat (default), got %v", cfg.HeartbeatInterval)
	}
	// Headless defaults to true
	if !boolIs(cfg.DefaultLaunchOpts.Headless) {
		t.Error("expected Headless=true by default")
	}
}

func TestLoadBrowserPoolConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "negative heartbeat",
			yaml:    "heartbeat_interval: -1m",
			wantErr: "heartbeat_interval",
		},
		{
			name:    "negative slots",
			yaml:    "providers:\n  deepseek:\n    slots: -3",
			wantErr: "slots must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "bad.yaml")
			if err := os.WriteFile(f, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadBrowserPoolConfig(f)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestBrowserPoolConfig_ConfigPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "browser_pool.yaml")
	if err := os.WriteFile(f, []byte("providers:\n  deepseek:\n    slots: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ConfigPath() != f {
		t.Errorf("expected config path %q, got %q", f, cfg.ConfigPath())
	}

	// No file -> empty path
	// Change to a temp directory so viper won't find browser_pool.yaml at the project root.
	origDir, _ := os.Getwd()
	emptyDir := t.TempDir()
	if err := os.Chdir(emptyDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	cfg2, err := LoadBrowserPoolConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.ConfigPath() != "" {
		t.Errorf("expected empty config path, got %q", cfg2.ConfigPath())
	}
}

func TestLoadBrowserPoolConfig_ExtraArgs(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "args.yaml")
	yamlContent := `
providers:
  deepseek:
    slots: 1
default_launch_opts:
  extra_args:
    - "--disable-gpu"
    - "--no-sandbox"
`
	if err := os.WriteFile(f, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedArgs := []string{"--disable-gpu", "--no-sandbox"}
	if len(cfg.DefaultLaunchOpts.ExtraArgs) != len(expectedArgs) {
		t.Fatalf("expected %d extra args, got %d", len(expectedArgs), len(cfg.DefaultLaunchOpts.ExtraArgs))
	}
	for i, arg := range expectedArgs {
		if cfg.DefaultLaunchOpts.ExtraArgs[i] != arg {
			t.Errorf("extra arg %d: expected %q, got %q", i, arg, cfg.DefaultLaunchOpts.ExtraArgs[i])
		}
	}

	// Per-provider should inherit the extra args from default launch opts
	for name, p := range cfg.Providers {
		if len(p.LaunchOpts.ExtraArgs) != len(expectedArgs) {
			t.Errorf("provider %q: expected %d extra args, got %d", name, len(expectedArgs), len(p.LaunchOpts.ExtraArgs))
		}
	}
}

func TestLoadBrowserPoolConfig_NoProviders(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "no_providers.yaml")
	yamlContent := `heartbeat_interval: 1m`
	if err := os.WriteFile(f, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still have default providers since none were specified
	if len(cfg.Providers) == 0 {
		t.Error("expected at least default providers when none are specified")
	}
	if _, ok := cfg.Providers["deepseek"]; !ok {
		t.Error("expected deepseek as default provider")
	}
}

func TestLoadBrowserPoolConfig_ProviderLaunchOptsOverride(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "provider_opts.yaml")
	yamlContent := `
heartbeat_interval: 30s
providers:
  deepseek:
    slots: 2
    launch_opts:
      headless: true
      window_size: "800x600"
  mimo:
    slots: 1
`
	if err := os.WriteFile(f, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// deepseek has its own launch_opts
	ds := cfg.Providers["deepseek"]
	if !boolIs(ds.LaunchOpts.Headless) {
		t.Error("deepseek: expected Headless=true (overridden)")
	}
	if ds.LaunchOpts.WindowSize != "800x600" {
		t.Errorf("deepseek: expected WindowSize 800x600, got %q", ds.LaunchOpts.WindowSize)
	}

	// mimo has no launch_opts — should inherit defaults
	mm := cfg.Providers["mimo"]
	if !boolIs(mm.LaunchOpts.Headless) {
		t.Error("mimo: expected Headless=true (inherited from defaults)")
	}
	if mm.LaunchOpts.WindowSize != "1920x1080" {
		t.Errorf("mimo: expected WindowSize 1920x1080, got %q", mm.LaunchOpts.WindowSize)
	}
}

func TestLoadBrowserPoolConfig_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(f, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBrowserPoolConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected 5m heartbeat on empty config, got %v", cfg.HeartbeatInterval)
	}
	if len(cfg.Providers) != 2 {
		t.Errorf("expected 2 default providers on empty config, got %d", len(cfg.Providers))
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
