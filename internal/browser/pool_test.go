package browser

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/higor/free-llm-hack-proxy/internal/config"
)

// =============================================================================
// Mock browser and factory
// =============================================================================

// mockBrowser implements the Browser interface without needing a real
// browser process.
type mockBrowser struct {
	id     int
	closed atomic.Bool
}

var mockBrowserCounter atomic.Int32

func newMockBrowser() *mockBrowser {
	return &mockBrowser{id: int(mockBrowserCounter.Add(1))}
}

func (m *mockBrowser) Close() error {
	m.closed.Store(true)
	return nil
}

func (m *mockBrowser) Ping(_ context.Context) error {
	return nil
}

// mockFactory tracks how many browsers were created and can simulate launch
// failures.
type mockFactory struct {
	launched atomic.Int32
	failOn   int32 // if > 0, fail after creating this many browsers
}

func newMockFactory() *mockFactory {
	return &mockFactory{}
}

func (m *mockFactory) NewBrowser(_ context.Context, _ *config.BrowserLaunchOptions) (Browser, error) {
	n := m.launched.Add(1)
	if m.failOn > 0 && n > m.failOn {
		return nil, fmt.Errorf("mock: simulated launch failure at browser #%d", n)
	}
	// wrap in mockBrowser for safe Close()
	return newMockBrowser(), nil
}

// count returns how many browsers the factory has created so far.
func (m *mockFactory) count() int {
	return int(m.launched.Load())
}

// boolPtr returns a pointer to a bool value (test helper).
func boolPtr(b bool) *bool {
	return &b
}

// =============================================================================
// Tests
// =============================================================================

func TestNewPool_CorrectBrowserCount(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: unexpected error: %v", err)
	}
	defer p.Close()

	// 2 providers × 1 slot each = 2 browsers total
	if want := 2; mock.count() != want {
		t.Errorf("expected %d browsers launched, got %d", want, mock.count())
	}

	stats := p.Stats()
	if stats.TotalBrowsers != 2 {
		t.Errorf("expected TotalBrowsers=2, got %d", stats.TotalBrowsers)
	}
	if len(stats.Providers) != 2 {
		t.Errorf("expected 2 providers in stats, got %d", len(stats.Providers))
	}
	for name, ps := range stats.Providers {
		if ps.ConfiguredSlots != 1 {
			t.Errorf("provider %q: expected 1 slot, got %d", name, ps.ConfiguredSlots)
		}
		if ps.AvailableSlots != 1 {
			t.Errorf("provider %q: expected 1 available, got %d", name, ps.AvailableSlots)
		}
	}
}

func TestNewPool_CustomConfig(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 3},
			"mimo":     {Slots: 2},
			"testai":   {Slots: 5},
		},
		HeartbeatInterval: 1 * time.Minute,
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless:   boolPtr(true),
			WindowSize: "1920x1080",
		},
	}
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: unexpected error: %v", err)
	}
	defer p.Close()

	if want := 10; mock.count() != want {
		t.Errorf("expected %d browsers (3+2+5), got %d", want, mock.count())
	}

	stats := p.Stats()
	if stats.TotalBrowsers != 10 {
		t.Errorf("expected TotalBrowsers=10, got %d", stats.TotalBrowsers)
	}
}

func TestNewPool_ZeroSlots(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 0},
		},
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool with 0 slots: unexpected error: %v", err)
	}
	defer p.Close()

	if mock.count() != 0 {
		t.Errorf("expected 0 browsers launched, got %d", mock.count())
	}
}

func TestNewPool_LaunchFailure(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 3},
		},
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}
	mock := newMockFactory()
	mock.failOn = 1 // succeed once, then fail on attempt #2

	_, err := newPool(cfg, mock)
	if err == nil {
		t.Fatal("expected error from launch failure, got nil")
	}
	// Factory was called twice: #1 succeeded, #2 failed. The first browser
	// was closed during cleanup.
	if mock.count() != 2 {
		t.Errorf("expected 2 factory calls (1 success + 1 failure), got %d", mock.count())
	}
}

func TestAcquire_Release(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// Acquire a browser for deepseek.
	b1, err := p.Acquire("deepseek")
	if err != nil {
		t.Fatalf("Acquire(deepseek): unexpected error: %v", err)
	}
	if b1 == nil {
		t.Fatal("Acquire: got nil browser")
	}
	stats := p.Stats()
	// After acquire, deepseek should have 0 available (1 checked out).
	if ds := stats.Providers["deepseek"]; ds.AvailableSlots != 0 {
		t.Errorf("after acquire: expected 0 available deepseek slots, got %d", ds.AvailableSlots)
	}
	if stats.TotalBrowsers != 1 {
		t.Errorf("after acquire: expected TotalBrowsers=1 (one checked out, one still available for mimo), got %d", stats.TotalBrowsers)
	}

	// Release it back.
	p.Release("deepseek", b1)
	stats = p.Stats()
	if ds := stats.Providers["deepseek"]; ds.AvailableSlots != 1 {
		t.Errorf("after release: expected 1 available deepseek slot, got %d", ds.AvailableSlots)
	}
	if stats.TotalBrowsers != 2 {
		t.Errorf("after release: expected TotalBrowsers=2, got %d", stats.TotalBrowsers)
	}
}

func TestAcquire_UnknownProvider(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	_, err = p.Acquire("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestRelease_NilBrowser(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// Should not panic.
	p.Release("deepseek", nil)

	stats := p.Stats()
	if stats.TotalBrowsers != 2 {
		t.Errorf("after nil release: expected TotalBrowsers=2, got %d", stats.TotalBrowsers)
	}
}

func TestAcquire_BlockUntilRelease(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 1},
		},
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// Consume the only slot.
	b1, err := p.Acquire("deepseek")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Try to acquire again — should block briefly. Use a goroutine to release
	// the browser after 50ms, letting the second acquire succeed.
	released := make(chan bool)
	go func() {
		time.Sleep(50 * time.Millisecond)
		p.Release("deepseek", b1)
		close(released)
	}()

	b2, err := p.Acquire("deepseek")
	if err != nil {
		t.Fatalf("second acquire (should have unblocked): %v", err)
	}
	if b2 != b1 {
		t.Error("expected the same browser instance after release-and-reacquire")
	}

	<-released
}

func TestClose_DrainsAllBrowsers(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}

	// Close and verify browsers are gone.
	p.Close()

	// After close, Acquire should fail.
	_, err = p.Acquire("deepseek")
	if err == nil {
		t.Error("expected error acquiring from closed pool")
	}

	stats := p.Stats()
	if stats.TotalBrowsers != 0 {
		t.Errorf("after close: expected TotalBrowsers=0, got %d", stats.TotalBrowsers)
	}
}

func TestClose_Idempotent(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}

	// Close multiple times — should not panic.
	p.Close()
	p.Close()
	p.Close()
}

func TestRelease_AfterClose(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}

	b1, err := p.Acquire("deepseek")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	p.Close()

	// Release after close — browser should be closed, not re-queued.
	// Should not panic or block.
	p.Release("deepseek", b1)
}

func TestInitGlobalPool_Singleton(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	mock1 := newMockFactory()
	mock2 := newMockFactory()

	// First init.
	p1, err := InitGlobalPool(cfg, mock1)
	if err != nil {
		t.Fatalf("first InitGlobalPool: %v", err)
	}
	if p1 == nil {
		t.Fatal("first InitGlobalPool: got nil pool")
	}
	if mock1.count() != 2 {
		t.Errorf("first init: expected 2 browsers, got %d", mock1.count())
	}

	// Second init with a different factory — should return the existing pool.
	p2, err := InitGlobalPool(cfg, mock2)
	if err != nil {
		t.Fatalf("second InitGlobalPool: %v", err)
	}
	if p2 != p1 {
		t.Error("second InitGlobalPool returned a different pool (not singleton)")
	}
	// mock2 should NOT have created any browsers.
	if mock2.count() != 0 {
		t.Errorf("second init created new browsers (want 0, got %d)", mock2.count())
	}

	// Cleanup.
	p1.Close()
	// Reset global state for other tests.
	globalPoolMu.Lock()
	globalPool = nil
	globalPoolMu.Unlock()
}

// =============================================================================
// Heartbeat tests
// =============================================================================

// pingTracker wraps a mockBrowser and counts Ping calls.
type pingTracker struct {
	*mockBrowser
	pingCount atomic.Int32
}

func (p *pingTracker) Ping(_ context.Context) error {
	p.pingCount.Add(1)
	return nil
}

func TestHeartbeat_IntervalZero_disabled(t *testing.T) {
	cfg := config.DefaultBrowserPoolConfig()
	cfg.HeartbeatInterval = 0 // explicitly disabled
	mock := newMockFactory()

	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// A zero-interval heartbeat should not start a goroutine, so WaitDone
	// should still work.
	p.Close()
	p.WaitDone()
}

func TestHeartbeat_PingsIdleBrowsers(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 2},
		},
		HeartbeatInterval: 10 * time.Millisecond,
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}

	// Override factory to return pingTrackers.
	factory := &trackingFactory{}
	p, err := newPool(cfg, factory)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// Wait for at least one heartbeat tick.
	time.Sleep(50 * time.Millisecond)

	// Both browsers should have been pinged at least once.
	idle := p.Stats().Providers["deepseek"].AvailableSlots
	if idle != 2 {
		t.Errorf("expected 2 idle deepseek browsers, got %d", idle)
	}

	for i, br := range factory.created {
		pt := br.(*pingTracker)
		n := pt.pingCount.Load()
		if n < 1 {
			t.Errorf("browser #%d was never pinged (count=%d)", i, n)
		}
	}
}

// trackingFactory creates pingTracker browsers so we can inspect Ping call counts.
type trackingFactory struct {
	mockFactory
	created []Browser
}

func (f *trackingFactory) NewBrowser(ctx context.Context, opts *config.BrowserLaunchOptions) (Browser, error) {
	mb := newMockBrowser()
	pt := &pingTracker{mockBrowser: mb}
	n := f.mockFactory.launched.Add(1)
	if f.mockFactory.failOn > 0 && n > f.mockFactory.failOn {
		return nil, fmt.Errorf("mock: simulated launch failure at browser #%d", n)
	}
	f.created = append(f.created, pt)
	return pt, nil
}

func TestHeartbeat_RemovesDeadBrowsers(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 2},
		},
		HeartbeatInterval: 10 * time.Millisecond,
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}

	factory := &flakyFactory{}
	p, err := newPool(cfg, factory)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	defer p.Close()

	// Wait for at least one heartbeat tick — flaky browsers should be removed.
	time.Sleep(50 * time.Millisecond)

	// The flaky pings return error, so both browsers should have been closed.
	stats := p.Stats()
	ds := stats.Providers["deepseek"]
	if ds.AvailableSlots > 0 {
		t.Errorf("expected 0 available deepseek slots (all browsers should have been removed by heartbeat), got %d", ds.AvailableSlots)
	}

	// All browsers should be marked closed.
	for _, b := range factory.created {
		if !b.closed.Load() {
			t.Error("expected dead browser to be closed by heartbeat")
		}
	}
}

// flakyBrowser fails every Ping.
type flakyBrowser struct {
	*mockBrowser
}

func (f *flakyBrowser) Ping(_ context.Context) error {
	return fmt.Errorf("simulated ping failure")
}

// flakyFactory creates flakyBrowser instances.
type flakyFactory struct {
	mockFactory
	created []*flakyBrowser
}

func (f *flakyFactory) NewBrowser(ctx context.Context, opts *config.BrowserLaunchOptions) (Browser, error) {
	mb := newMockBrowser()
	fb := &flakyBrowser{mockBrowser: mb}
	n := f.mockFactory.launched.Add(1)
	if f.mockFactory.failOn > 0 && n > f.mockFactory.failOn {
		return nil, fmt.Errorf("mock: simulated launch failure at browser #%d", n)
	}
	f.created = append(f.created, fb)
	return fb, nil
}

func TestClose_AfterHeartbeat_DrainsAllBrowsers(t *testing.T) {
	cfg := &config.BrowserPoolConfig{
		Providers: map[string]*config.ProviderBrowserConfig{
			"deepseek": {Slots: 2},
			"mimo":     {Slots: 1},
		},
		HeartbeatInterval: 10 * time.Millisecond,
		DefaultLaunchOpts: &config.BrowserLaunchOptions{
			Headless: boolPtr(true),
		},
	}

	mock := newMockFactory()
	p, err := newPool(cfg, mock)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}

	// Let the heartbeat run a few ticks.
	time.Sleep(60 * time.Millisecond)

	// Close and verify all browsers were drained.
	p.Close()
	p.WaitDone()

	stats := p.Stats()
	if stats.TotalBrowsers != 0 {
		t.Errorf("after close with heartbeat: expected TotalBrowsers=0, got %d", stats.TotalBrowsers)
	}

	// Acquire should fail.
	_, err = p.Acquire("deepseek")
	if err == nil {
		t.Error("expected error acquiring from closed pool after heartbeat")
	}
}

