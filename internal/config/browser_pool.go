package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

// BrowserLaunchOptions configures go-rod browser launch parameters for
// each browser instance in the pool.
type BrowserLaunchOptions struct {
	// Headless controls whether the browser runs in headless mode.
	// Uses *bool so we can distinguish "not set" (nil) from "explicitly false".
	Headless *bool `mapstructure:"headless" yaml:"headless"`

	// UserDataDir is an optional path to a persistent browser profile directory.
	UserDataDir string `mapstructure:"user_data_dir" yaml:"user_data_dir"`

	// Proxy sets an HTTP/SOCKS proxy URL for the browser (e.g. "http://127.0.0.1:8080").
	Proxy string `mapstructure:"proxy" yaml:"proxy"`

	// WindowSize sets the browser viewport size (e.g. "1920x1080").
	WindowSize string `mapstructure:"window_size" yaml:"window_size"`

	// ExtraArgs are additional Chromium command-line flags passed to chromedp.
	ExtraArgs []string `mapstructure:"extra_args" yaml:"extra_args"`
}

// ProviderBrowserConfig defines browser pool sizing and launch options for a
// single LLM provider.
type ProviderBrowserConfig struct {
	// Slots is the number of concurrent browser instances reserved for this provider.
	Slots int `mapstructure:"slots" yaml:"slots"`

	// LaunchOpts overrides the pool-wide defaults for this provider's instances.
	LaunchOpts *BrowserLaunchOptions `mapstructure:"launch_opts" yaml:"launch_opts"`
}

// BrowserPoolConfig holds all configuration for the browser pool manager.
type BrowserPoolConfig struct {
	// Providers maps provider names to their per-provider browser config.
	// Example: "deepseek" -> { slots: 2 }
	Providers map[string]*ProviderBrowserConfig `mapstructure:"providers" yaml:"providers"`

	// HeartbeatInterval controls how often each browser instance emits a
	// liveness signal. Default: 5 minutes.
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval" yaml:"heartbeat_interval"`

	// DefaultLaunchOpts are the baseline browser launch options; per-provider
	// configs can override individual fields.
	DefaultLaunchOpts *BrowserLaunchOptions `mapstructure:"default_launch_opts" yaml:"default_launch_opts"`

	// configPath stores the path from which this config was loaded, used for
	// error messages and re-reading support.
	configPath string
}

// =============================================================================
// Defaults
// =============================================================================

// DefaultBrowserPoolConfig returns a fully-populated BrowserPoolConfig with
// all fields set to sensible defaults. Callers should call LoadBrowserPoolConfig
// and merge over these defaults.
func DefaultBrowserPoolConfig() *BrowserPoolConfig {
	return &BrowserPoolConfig{
		Providers: map[string]*ProviderBrowserConfig{
			"deepseek": {Slots: 1},
			"mimo":     {Slots: 1},
		},
		HeartbeatInterval: 5 * time.Minute,
		DefaultLaunchOpts: &BrowserLaunchOptions{
			Headless:   boolPtr(true),
			WindowSize: "1920x1080",
			ExtraArgs:  []string{},
		},
	}
}

// =============================================================================
// Loading
// =============================================================================

// LoadBrowserPoolConfig reads a YAML config file and returns a validated
// BrowserPoolConfig. Missing fields are filled from defaults.
//
// The configPath can be:
//   - A path to a YAML file (e.g. "/etc/proxy/browser_pool.yaml")
//   - Empty string "" — attempts to read from the default locations in order:
//     1. ./browser_pool.yaml   (project-root convention)
//     2. The BROWSER_POOL_CONFIG environment variable
func LoadBrowserPoolConfig(configPath string) (*BrowserPoolConfig, error) {
	v := viper.New()
	v.SetConfigType("yaml")

	// Set defaults so viper can distinguish "explicitly set" from "missing".
	defaults := DefaultBrowserPoolConfig()
	v.SetDefault("heartbeat_interval", defaults.HeartbeatInterval)
	setDefaultLaunchOpts(v, "default_launch_opts", defaults.DefaultLaunchOpts)

	// Resolve the config path.
	resolvedPath := configPath
	if resolvedPath == "" {
		resolvedPath = os.Getenv("BROWSER_POOL_CONFIG")
	}

	// Only call SetConfigFile if the path actually exists; otherwise fall
	// through to AddConfigPath so non-existent paths are silently treated
	// as "no user config" and return defaults.
	if resolvedPath != "" {
		if _, statErr := os.Stat(resolvedPath); statErr == nil {
			v.SetConfigFile(resolvedPath)
		} else if errors.Is(statErr, os.ErrNotExist) {
			// Path doesn't exist — silently use defaults.
			resolvedPath = ""
		} else {
			return nil, fmt.Errorf("browser pool config: stat %q: %w", resolvedPath, statErr)
		}
	}

	if resolvedPath == "" {
		v.AddConfigPath(".")
		v.SetConfigName("browser_pool")
	}

	// Read the file; it's fine if none exists.
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("browser pool config: read: %w", err)
		}
	}

	// Unmarshal into a raw config.
	loaded := &BrowserPoolConfig{}
	if err := v.Unmarshal(loaded, viper.DecodeHook(
		mapstructure.StringToTimeDurationHookFunc(),
	)); err != nil {
		return nil, fmt.Errorf("browser pool config: unmarshal: %w", err)
	}

	// --- Build the final config, layer by layer: pool first, then providers.

	cfg := &BrowserPoolConfig{
		configPath: resolvedPath,
	}

	// HeartbeatInterval
	if v.IsSet("heartbeat_interval") {
		cfg.HeartbeatInterval = loaded.HeartbeatInterval
	} else {
		cfg.HeartbeatInterval = defaults.HeartbeatInterval
	}

	// DefaultLaunchOpts — build the pool-wide default first so per-provider
	// configs can inherit from it.
	cfg.DefaultLaunchOpts = mergePoolLaunchOpts(defaults.DefaultLaunchOpts, loaded.DefaultLaunchOpts, v, "default_launch_opts")

	// Per-provider configs — inherit from the merged pool-wide defaults.
	cfg.Providers = buildProviders(defaults, loaded, cfg.DefaultLaunchOpts, v)

	// --- validation ---------------------------------------------------------
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("browser pool config: %w", err)
	}

	return cfg, nil
}

// =============================================================================
// Internal loader helpers
// =============================================================================

// setDefaultLaunchOpts registers viper defaults for a BrowserLaunchOptions
// subtree at the given key prefix.
func setDefaultLaunchOpts(v *viper.Viper, prefix string, opts *BrowserLaunchOptions) {
	if opts == nil {
		return
	}
	if opts.Headless != nil {
		v.SetDefault(prefix+".headless", *opts.Headless)
	}
	if opts.WindowSize != "" {
		v.SetDefault(prefix+".window_size", opts.WindowSize)
	}
	if len(opts.ExtraArgs) > 0 {
		v.SetDefault(prefix+".extra_args", opts.ExtraArgs)
	}
}

// mergePoolLaunchOpts merges file-loaded opts over the built-in defaults.
// Uses IsSet on each field to distinguish "false" from "absent".
func mergePoolLaunchOpts(base *BrowserLaunchOptions, src *BrowserLaunchOptions, v *viper.Viper, prefix string) *BrowserLaunchOptions {
	out := &BrowserLaunchOptions{}
	if base != nil {
		*out = *base // shallow copy
	}
	if src == nil {
		return out
	}
	if v.IsSet(prefix + ".headless") {
		out.Headless = src.Headless
	}
	if v.IsSet(prefix + ".user_data_dir") {
		out.UserDataDir = src.UserDataDir
	}
	if v.IsSet(prefix + ".proxy") {
		out.Proxy = src.Proxy
	}
	if v.IsSet(prefix + ".window_size") {
		out.WindowSize = src.WindowSize
	}
	if v.IsSet(prefix + ".extra_args") {
		if len(src.ExtraArgs) > 0 {
			out.ExtraArgs = append([]string{}, src.ExtraArgs...)
		} else {
			out.ExtraArgs = []string{}
		}
	}
	return out
}

// buildProviders merges user-provided provider configs over the defaults.
// poolLaunchOpts is the already-merged pool-wide default launch options.
func buildProviders(defaults *BrowserPoolConfig, loaded *BrowserPoolConfig, poolLaunchOpts *BrowserLaunchOptions, v *viper.Viper) map[string]*ProviderBrowserConfig {
	out := make(map[string]*ProviderBrowserConfig)

	// Start with defaults for all known providers; each gets a copy of the
	// pool-wide launch opts.
	for name, p := range defaults.Providers {
		cfg := &ProviderBrowserConfig{Slots: p.Slots}
		if poolLaunchOpts != nil {
			cp := *poolLaunchOpts
			cfg.LaunchOpts = &cp
		}
		out[name] = cfg
	}

	// Overlay user-provided configs.
	if loaded == nil || loaded.Providers == nil {
		return out
	}
	for name, p := range loaded.Providers {
		if p == nil {
			continue
		}
		prefix := "providers." + name

		cfg, exists := out[name]
		if !exists {
			cfg = &ProviderBrowserConfig{}
			out[name] = cfg
		}

		if v.IsSet(prefix + ".slots") {
			cfg.Slots = p.Slots
		}

		if v.IsSet(prefix + ".launch_opts") {
			// User specified per-provider launch opts — merge over pool defaults.
			base := poolLaunchOpts
			cfg.LaunchOpts = mergePoolLaunchOpts(base, p.LaunchOpts, v, prefix+".launch_opts")
		} else if cfg.LaunchOpts == nil {
			// No provider opts and no inherited pool opts — use a copy of pool.
			if poolLaunchOpts != nil {
				cp := *poolLaunchOpts
				cfg.LaunchOpts = &cp
			}
		}
	}

	return out
}

// =============================================================================
// Validation
// =============================================================================

func (c *BrowserPoolConfig) validate() error {
	if c.HeartbeatInterval < 0 {
		return fmt.Errorf("heartbeat_interval must be non-negative (got %v)", c.HeartbeatInterval)
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}
	for name, p := range c.Providers {
		if p == nil {
			return fmt.Errorf("provider %q: config is nil", name)
		}
		if p.Slots < 0 {
			return fmt.Errorf("provider %q: slots must be non-negative (got %d)", name, p.Slots)
		}
	}
	return nil
}

// =============================================================================
// Public accessors
// =============================================================================

// ConfigPath returns the path from which this config was loaded (or "" if
// loaded from defaults only).
func (c *BrowserPoolConfig) ConfigPath() string {
	return c.configPath
}

// =============================================================================
// Helpers
// =============================================================================

func boolPtr(b bool) *bool {
	return &b
}
