package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func guardedConfig(interval time.Duration) config.Config {
	cfg := config.Default()
	cfg.Egress.RequireNamespaceGuard = true
	cfg.Egress.TunnelInterface = "wg0"
	cfg.Egress.ProbeInterval = interval
	return cfg
}

// A tunnel that stops verifying ends the process.
//
// Verifying once at startup is not enough, and the gap is not theoretical: when
// the tunnel container restarts, this process stays in the namespace that was
// destroyed. Observed in a real container — still "Up", no default route, no
// eth0, silent, and doing nothing, while `restart: unless-stopped` never fired
// because it never exited.
func TestTheDownloaderStopsWhenTheTunnelStopsVerifying(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) (bool, string) {
		if calls.Add(1) < 3 {
			return true, "fine"
		}
		return false, "the tunnel went away"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := watchEgress(ctx, guardedConfig(10*time.Millisecond), quietLog(), probe)
	if err == nil {
		t.Fatal("the watcher returned nil after the tunnel stopped verifying; " +
			"the process would keep running with no way to egress and the " +
			"restart policy would never fire")
	}
	if !errors.Is(err, egress.ErrNotJailed) {
		t.Errorf("err = %v, want ErrNotJailed", err)
	}
	if got := calls.Load(); got < 3 {
		t.Errorf("the probe ran %d times; it should keep checking, not check once", got)
	}
}

// A healthy tunnel is not a reason to exit, and the watcher returns cleanly on
// shutdown rather than making every SIGTERM look like a failure.
func TestAHealthyTunnelRunsUntilShutdown(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) (bool, string) {
		calls.Add(1)
		return true, "fine"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	if err := watchEgress(ctx, guardedConfig(10*time.Millisecond), quietLog(), probe); err != nil {
		t.Errorf("a healthy tunnel produced an error: %v", err)
	}
	if calls.Load() == 0 {
		t.Error("the watcher never probed at all")
	}
}

// A probe that fails BECAUSE the process is shutting down is not a tunnel
// failure. Without this, every clean stop exits non-zero and an operator
// reading `docker ps -a` cannot tell an orderly shutdown from a kill switch.
func TestAProbeCancelledByShutdownIsNotAFailure(t *testing.T) {
	// A DEADLINE as well as the cancel.
	//
	// The first version of this test had only the cancel, which the probe
	// itself triggers — so under a mutation where the probe is never called,
	// it looped forever instead of failing. A test that hangs rather than
	// fails reports nothing at all, which is worse than one that is simply
	// wrong: the wrong one at least tells you something.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var probed atomic.Bool
	probe := func(context.Context) (bool, string) {
		probed.Store(true)
		cancel()
		return false, "context canceled"
	}

	if err := watchEgress(ctx, guardedConfig(10*time.Millisecond), quietLog(), probe); err != nil {
		t.Errorf("a shutdown was reported as a tunnel failure: %v", err)
	}
	if !probed.Load() {
		t.Fatal("the probe was never called, so this proved nothing about how a " +
			"cancelled probe is treated")
	}
}

// An operator who turned the guard off is not re-checked, and is not woken by
// an alarm about a policy they switched off on purpose.
func TestWithTheGuardDisabledNothingIsProbed(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) (bool, string) {
		calls.Add(1)
		return false, "would have failed"
	}

	cfg := guardedConfig(10 * time.Millisecond)
	cfg.Egress.RequireNamespaceGuard = false

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	if err := watchEgress(ctx, cfg, quietLog(), probe); err != nil {
		t.Errorf("err = %v, want nil when the operator opted out", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the probe ran %d times with the guard disabled", got)
	}
}

// An unset interval must not mean "never".
func TestAnUnsetProbeIntervalDoesNotMeanNever(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) (bool, string) {
		calls.Add(1)
		return true, "fine"
	}

	cfg := guardedConfig(0)
	// Long enough that a sane floor has not fired, short enough that a busy
	// loop would run thousands of times.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if err := watchEgress(ctx, cfg, quietLog(), probe); err != nil {
		t.Errorf("err = %v", err)
	}
	if got := calls.Load(); got > 2 {
		t.Errorf("the probe ran %d times in 150ms: an unset interval became a "+
			"busy loop rather than a sensible floor", got)
	}
}
