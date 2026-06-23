package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/higor/free-llm-hack-proxy/internal/config"
)

// =============================================================================
// Singleton
// =============================================================================

var (
	globalPool   *Pool
	globalPoolMu sync.Mutex
)

// InitGlobalPool initialises (or returns) the global browser pool singleton.
// On first call it launches the configured number of browser instances per
// provider. Subsequent calls return the existing pool.
//
// cfg controls how many browsers per provider and their launch options.
// factory is how browser instances are created. Pass nil to use the real
// go-rod launcher factory.
//
// Close the pool with GetGlobalPool().Close() on shutdown.
func InitGlobalPool(cfg *config.BrowserPoolConfig, factory BrowserFactory) (*Pool, error) {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	if globalPool != nil {
		return globalPool, nil
	}

	if factory == nil {
		factory = NewRodFactory(cfg)
	}

	p, err := newPool(cfg, factory)
	if err != nil {
		return nil, err
	}
	globalPool = p
	return p, nil
}

// GetGlobalPool returns the global pool singleton, or nil if not yet
// initialised.
func GetGlobalPool() *Pool {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()
	return globalPool
}

// =============================================================================
// Pool
// =============================================================================

// Browser is a browser instance that the pool manages. The production
// implementation uses *rod.Browser (wrapped as rodBrowser) which satisfies
// this interface. Tests provide lightweight mocks.
type Browser interface {
	Close() error
	// Ping performs a lightweight liveness check. The pool calls this
	// periodically on idle browsers to prevent WebSocket timeouts.
	Ping(ctx context.Context) error
}

// Pool manages a set of browser instances organised by provider name.
// It is a thread-safe singleton: each provider gets its own buffered channel
// of browsers, one per configured slot.
type Pool struct {
	mu      sync.Mutex
	cfg     *config.BrowserPoolConfig
	factory BrowserFactory

	// slots maps provider name → buffered channel of ready browsers.
	// The channel capacity equals the configured slot count.
	slots map[string]chan Browser
	// total tracks how many live browser processes exist across all providers.
	total atomic.Int32

	quit  chan struct{}
	done  chan struct{}
	closeOnce sync.Once

	// heartbeatWg tracks the heartbeat goroutine so Close() can wait for
	// it to exit before draining browsers.
	heartbeatWg sync.WaitGroup
}

// poolStats is returned by Stats() for inspection and testing.
type poolStats struct {
	TotalBrowsers  int
	Providers      map[string]providerStats
}

type providerStats struct {
	ConfiguredSlots int
	AvailableSlots  int // browsers sitting idle in the channel
}

func newPool(cfg *config.BrowserPoolConfig, factory BrowserFactory) (*Pool, error) {
	p := &Pool{
		cfg:     cfg,
		factory: factory,
		slots:   make(map[string]chan Browser, len(cfg.Providers)),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	ctx := context.Background()

	for name, providerCfg := range cfg.Providers {
		if providerCfg == nil {
			continue
		}
		cap := providerCfg.Slots
		if cap < 0 {
			cap = 0
		}

		ch := make(chan Browser, cap)
		p.slots[name] = ch

		for i := 0; i < cap; i++ {
			opts := providerCfg.LaunchOpts
			if opts == nil {
				opts = cfg.DefaultLaunchOpts
			}
			browser, err := factory.NewBrowser(ctx, opts)
			if err != nil {
				p.close()
				return nil, fmt.Errorf("pool init: launch %s[%d/%d]: %w", name, i+1, cap, err)
			}
			ch <- browser
			p.total.Add(1)
		}

		log.Printf("browser pool: %q launched %d/%d browser(s)", name, cap, cap)
	}

	log.Printf("browser pool: total %d browser(s) across %d provider(s)",
		p.total.Load(), len(cfg.Providers))

	p.startHeartbeat()

	return p, nil
}

// Acquire returns a browser instance for the given provider. It blocks until
// one is available or the pool is closed. Returns an error if the provider
// does not exist or the pool has been shut down.
func (p *Pool) Acquire(provider string) (Browser, error) {
	ch, err := p.channelFor(provider)
	if err != nil {
		return nil, err
	}

	select {
	case b := <-ch:
		p.total.Add(-1) // moved from "available in channel" → "checked out"
		return b, nil
	case <-p.quit:
		return nil, errors.New("browser pool: closed")
	}
}

// Release returns a browser instance to the pool. It is safe to call from
// multiple goroutines. Calling Release after the pool has been closed is a
// no-op (the browser is closed instead of re-queued).
func (p *Pool) Release(provider string, browser Browser) {
	if browser == nil {
		return
	}
	ch, err := p.channelFor(provider)
	if err != nil {
		browser.Close()
		return
	}

	select {
	case ch <- browser:
		p.total.Add(1) // back in the channel
	case <-p.quit:
		browser.Close()
	}
}

// Stats returns a snapshot of pool health without blocking.
func (p *Pool) Stats() *poolStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	s := &poolStats{
		TotalBrowsers: int(p.total.Load()),
		Providers:     make(map[string]providerStats, len(p.slots)),
	}

	for name, ch := range p.slots {
		cfg := p.cfg.Providers[name]
		slots := 0
		if cfg != nil {
			slots = cfg.Slots
		}
		s.Providers[name] = providerStats{
			ConfiguredSlots: slots,
			AvailableSlots:  len(ch),
		}
	}
	return s
}

// startHeartbeat launches a background goroutine that periodically pings
// every idle browser to prevent WebSocket or OS-level timeouts. It honours
// the configured HeartbeatInterval; a zero or negative interval disables it.
func (p *Pool) startHeartbeat() {
	if p.cfg.HeartbeatInterval <= 0 {
		return
	}

	p.heartbeatWg.Add(1)
	go func() {
		defer p.heartbeatWg.Done()

		ticker := time.NewTicker(p.cfg.HeartbeatInterval)
		defer ticker.Stop()

		log.Printf("browser pool heartbeat: started (interval=%v)", p.cfg.HeartbeatInterval)

		for {
			select {
			case <-ticker.C:
				p.doHeartbeat()
			case <-p.quit:
				log.Print("browser pool heartbeat: stopped")
				return
			}
		}
	}()
}

// doHeartbeat drains each provider channel, pings every browser, and
// re-queues them. Browsers that fail Ping are closed and not re-queued.
// The pool mutex serialises this with Close() so draining is race-free.
func (p *Pool) doHeartbeat() {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for name, ch := range p.slots {
		// Non-blocking drain: collect every browser currently idle.
		var buf []Browser
	loop:
		for {
			select {
			case b := <-ch:
				buf = append(buf, b)
			default:
				break loop
			}
		}

		if len(buf) == 0 {
			continue
		}

		// Ping each browser; close and drop failed ones.
		alive := buf[:0]
		for _, b := range buf {
			if err := b.Ping(ctx); err != nil {
				log.Printf("browser pool heartbeat: %q browser ping failed: %v — closing", name, err)
				b.Close()
				p.total.Add(-1)
			} else {
				alive = append(alive, b)
			}
		}

		// Re-queue survivors.
		for _, b := range alive {
			ch <- b
		}

		log.Printf("browser pool heartbeat: %q pinged %d browser(s), closed %d stale",
			name, len(alive), len(buf)-len(alive))
	}
}

// Close shuts down every browser in the pool. Safe to call multiple times.
func (p *Pool) Close() {
	p.close()
}

func (p *Pool) close() {
	p.closeOnce.Do(func() {
		close(p.quit)

		// Wait for the heartbeat goroutine to exit before draining so we
		// don't race with a mid-tick re-queue.
		p.heartbeatWg.Wait()

		p.mu.Lock()
		defer p.mu.Unlock()

		var closed int32
		for name, ch := range p.slots {
			// Drain the channel and close each browser.
		loop:
			for {
				select {
				case browser := <-ch:
					browser.Close()
					closed++
				default:
					break loop
				}
			}
			delete(p.slots, name)
		}
		p.total.Store(0)
		close(p.done)
		log.Printf("browser pool: closed %d browser(s)", closed)
	})
}

// WaitDone blocks until the pool is fully closed.
func (p *Pool) WaitDone() {
	<-p.done
}

// channelFor returns the provider's channel or an error if the pool is closed
// or the provider is unknown.
func (p *Pool) channelFor(provider string) (chan Browser, error) {
	p.mu.Lock()
	ch, ok := p.slots[provider]
	p.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("browser pool: unknown provider %q", provider)
	}
	return ch, nil
}
