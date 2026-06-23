// Package config loads and validates configuration from YAML files, env vars,
// and default values.
//
// The package supports two configuration domains:
//   - Browser pool: defines go-rod instance management per LLM provider
//     (slots, heartbeat interval, launch options). See BrowserPoolConfig.
//
// Usage:
//
//	cfg, err := config.LoadBrowserPoolConfig("/path/to/browser_pool.yaml")
//	if err != nil { ... }
//	fmt.Println(cfg.Providers["deepseek"].Slots)
package config
