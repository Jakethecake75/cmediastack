package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/platform/config"
)

// A restart from the web (ADR-0065): the same graceful stop as a signal, then
// an exit status the supervisor brings back.

func TestARestartRequestEndsRunWithErrRestart(t *testing.T) {
	ctx, trigger, requested := restartable(context.Background())
	if requested() || ctx.Err() != nil {
		t.Fatal("restart requested before anyone asked")
	}
	trigger()
	if !requested() {
		t.Error("the trigger did not record the request")
	}
	if ctx.Err() == nil {
		t.Error("the trigger did not end the context the servers wait on")
	}

	parent, cancel := context.WithCancel(context.Background())
	ctx2, _, requested2 := restartable(parent)
	cancel() // a signal, not a restart
	<-ctx2.Done()
	if requested2() {
		t.Error("a signal was taken for a restart")
	}
}

func TestExitStatusForARestartIsThree(t *testing.T) {
	lintErr := config.Lint(config.Config{}, func(string) string { return "" })
	if _, ok := config.AsLintError(lintErr); !ok {
		t.Fatalf("precondition: an empty config should fail the lint, got %v", lintErr)
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 0}, {errRestart, 3}, {lintErr, 78}, {errors.New("boom"), 1}} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
