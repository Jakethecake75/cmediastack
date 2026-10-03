package tasks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestScheduler() *Scheduler { return New(quietLogger(), nil) }

func TestTriggerRunsAndRecordsSuccess(t *testing.T) {
	s := newTestScheduler()
	var ran atomic.Int32

	s.Register(Task{
		Name: "purge", Description: "purge things",
		Run: func(context.Context) (string, error) {
			ran.Add(1)
			return "removed 3 rows", nil
		},
	})

	out, err := s.Trigger(t.Context(), "purge")
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !out.Succeeded() {
		t.Errorf("outcome failed: %s", out.Err)
	}
	if out.Summary != "removed 3 rows" {
		t.Errorf("summary = %q", out.Summary)
	}
	if !out.Manual {
		t.Error("a triggered run should be marked manual")
	}
	if ran.Load() != 1 {
		t.Errorf("ran %d times", ran.Load())
	}

	st := s.Snapshot()
	if len(st) != 1 {
		t.Fatalf("snapshot has %d tasks", len(st))
	}
	if st[0].Runs != 1 || st[0].Failures != 0 {
		t.Errorf("runs=%d failures=%d", st[0].Runs, st[0].Failures)
	}
	if st[0].LastRun == nil {
		t.Error("last run was not recorded")
	}
	if len(st[0].History) != 1 {
		t.Errorf("history has %d entries", len(st[0].History))
	}
}

func TestFailureIsRecordedWithItsError(t *testing.T) {
	s := newTestScheduler()
	s.Register(Task{Name: "broken", Run: func(context.Context) (string, error) {
		return "", errors.New("database is on fire")
	}})

	out, err := s.Trigger(t.Context(), "broken")
	if err != nil {
		t.Fatalf("trigger returned an error for a failing task: %v", err)
	}
	if out.Succeeded() {
		t.Fatal("a failing task reported success")
	}
	if out.Err != "database is on fire" {
		t.Errorf("error = %q", out.Err)
	}

	st := s.Snapshot()[0]
	if st.Failures != 1 || st.Runs != 1 {
		t.Errorf("runs=%d failures=%d", st.Runs, st.Failures)
	}
}

// One bad task must not take the process down.
func TestPanicIsContainedAndRecordedAsFailure(t *testing.T) {
	s := newTestScheduler()
	s.Register(Task{Name: "exploding", Run: func(context.Context) (string, error) {
		panic("nil map write")
	}})

	out, err := s.Trigger(t.Context(), "exploding")
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if out.Succeeded() {
		t.Fatal("a panicking task reported success")
	}
	if out.Err != "panic: nil map write" {
		t.Errorf("error = %q, want the panic value", out.Err)
	}
	if s.Snapshot()[0].Failures != 1 {
		t.Error("the panic was not counted as a failure")
	}

	// The scheduler still works afterwards.
	s2 := s
	s2.Register(Task{Name: "fine", Run: func(context.Context) (string, error) { return "ok", nil }})
	if _, err := s2.Trigger(t.Context(), "fine"); err != nil {
		t.Errorf("the scheduler was left broken: %v", err)
	}
}

// Overlapping runs of a purge corrupt each other's assumptions, so a task must
// never run concurrently with itself.
func TestTaskNeverRunsConcurrentlyWithItself(t *testing.T) {
	s := newTestScheduler()

	release := make(chan struct{})
	started := make(chan struct{})
	var concurrent atomic.Int32
	var maxSeen atomic.Int32

	s.Register(Task{Name: "slow", Run: func(context.Context) (string, error) {
		n := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		concurrent.Add(-1)
		return "done", nil
	}})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.Trigger(t.Context(), "slow")
	}()
	<-started

	// A second trigger while the first is running is refused, not queued.
	if _, err := s.Trigger(t.Context(), "slow"); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("a concurrent trigger returned %v, want ErrAlreadyRunning", err)
	}

	close(release)
	wg.Wait()

	if maxSeen.Load() > 1 {
		t.Errorf("the task ran %d times concurrently", maxSeen.Load())
	}

	// Once finished, it can be triggered again.
	release2 := make(chan struct{})
	close(release2)
	if _, err := s.Trigger(t.Context(), "slow"); err != nil {
		t.Errorf("the task could not be re-triggered after finishing: %v", err)
	}
}

func TestUnknownTaskIsRefused(t *testing.T) {
	s := newTestScheduler()
	if _, err := s.Trigger(t.Context(), "no-such-task"); !errors.Is(err, ErrUnknownTask) {
		t.Errorf("got %v, want ErrUnknownTask", err)
	}
}

func TestScheduledRunsFireOnInterval(t *testing.T) {
	s := newTestScheduler()
	var ran atomic.Int32
	done := make(chan struct{})
	var once sync.Once

	s.Register(Task{Name: "ticker", Interval: 10 * time.Millisecond,
		Run: func(context.Context) (string, error) {
			if ran.Add(1) >= 2 {
				once.Do(func() { close(done) })
			}
			return "tick", nil
		}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		s.Stop()
		t.Fatalf("the task ran %d times in 3s, want at least 2", ran.Load())
	}

	cancel()
	s.Stop()

	st := s.Snapshot()[0]
	if st.NextRun == nil {
		t.Error("next run was not scheduled")
	}
	if st.Runs < 2 {
		t.Errorf("runs = %d", st.Runs)
	}
}

func TestStopWaitsForInFlightRuns(t *testing.T) {
	s := newTestScheduler()
	release := make(chan struct{})
	var finished atomic.Bool

	s.Register(Task{Name: "slow", Interval: 5 * time.Millisecond,
		Run: func(context.Context) (string, error) {
			<-release
			finished.Store(true)
			return "done", nil
		}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(30 * time.Millisecond) // let it start a run

	cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	s.Stop()

	if !finished.Load() {
		t.Error("Stop returned while a run was still in flight")
	}
}

// A task with no interval only runs when someone asks.
func TestManualOnlyTaskDoesNotSelfSchedule(t *testing.T) {
	s := newTestScheduler()
	var ran atomic.Int32
	s.Register(Task{Name: "manual", Run: func(context.Context) (string, error) {
		ran.Add(1)
		return "", nil
	}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	s.Stop()

	if ran.Load() != 0 {
		t.Errorf("a manual-only task ran %d times on its own", ran.Load())
	}
	if s.Snapshot()[0].NextRun != nil {
		t.Error("a manual-only task should have no next run")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := newTestScheduler()
	s.Register(Task{Name: "chatty", Run: func(context.Context) (string, error) { return "ok", nil }})

	for i := 0; i < historyDepth+15; i++ {
		if _, err := s.Trigger(t.Context(), "chatty"); err != nil {
			t.Fatal(err)
		}
	}

	st := s.Snapshot()[0]
	if len(st.History) != historyDepth {
		t.Errorf("history has %d entries, want %d", len(st.History), historyDepth)
	}
	if st.Runs != historyDepth+15 {
		t.Errorf("run count = %d, want %d", st.Runs, historyDepth+15)
	}
}

func TestRegistrationGuards(t *testing.T) {
	cases := map[string]func(*Scheduler){
		"empty name": func(s *Scheduler) { s.Register(Task{Run: func(context.Context) (string, error) { return "", nil }}) },
		"nil runner": func(s *Scheduler) { s.Register(Task{Name: "x"}) },
		"duplicate": func(s *Scheduler) {
			run := func(context.Context) (string, error) { return "", nil }
			s.Register(Task{Name: "dup", Run: run})
			s.Register(Task{Name: "dup", Run: run})
		},
		"after start": func(s *Scheduler) {
			s.Start(context.Background())
			s.Register(Task{Name: "late", Run: func(context.Context) (string, error) { return "", nil }})
		},
		// A manual-only task starts no timer, so running it at start would be
		// silently ignored.
		"at start with no interval": func(s *Scheduler) {
			s.Register(Task{Name: "never", RunAtStart: true,
				Run: func(context.Context) (string, error) { return "", nil }})
		},
	}

	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s was permitted", name)
				}
			}()
			fn(newTestScheduler())
		})
	}
}

func TestObserverSeesEveryRun(t *testing.T) {
	s := newTestScheduler()

	var mu sync.Mutex
	seen := map[string]int{}
	s.Observe(func(name string, o Outcome) {
		mu.Lock()
		defer mu.Unlock()
		if o.Succeeded() {
			seen[name+":ok"]++
		} else {
			seen[name+":fail"]++
		}
	})

	s.Register(Task{Name: "good", Run: func(context.Context) (string, error) { return "", nil }})
	s.Register(Task{Name: "bad", Run: func(context.Context) (string, error) { return "", errors.New("nope") }})

	_, _ = s.Trigger(t.Context(), "good")
	_, _ = s.Trigger(t.Context(), "good")
	_, _ = s.Trigger(t.Context(), "bad")

	mu.Lock()
	defer mu.Unlock()
	if seen["good:ok"] != 2 || seen["bad:fail"] != 1 {
		t.Errorf("observer saw %v", seen)
	}
}

func TestSnapshotIsSortedAndCopied(t *testing.T) {
	s := newTestScheduler()
	run := func(context.Context) (string, error) { return "ok", nil }
	s.Register(Task{Name: "zebra", Run: run})
	s.Register(Task{Name: "alpha", Run: run})

	_, _ = s.Trigger(t.Context(), "alpha")

	st := s.Snapshot()
	if st[0].Name != "alpha" || st[1].Name != "zebra" {
		t.Errorf("not sorted: %s, %s", st[0].Name, st[1].Name)
	}

	// Mutating the snapshot must not affect the scheduler.
	st[0].History = nil
	if len(s.Snapshot()[0].History) != 1 {
		t.Error("the snapshot aliased the scheduler's history")
	}

	if names := s.Names(); len(names) != 2 || names[0] != "alpha" {
		t.Errorf("Names() = %v", names)
	}
}

// A task whose schedule lives on disk runs as the scheduler starts, not an
// interval later: the ticker restarts with the process, and an instance
// restarted more often than the interval would otherwise never run it.
func TestATaskCanRunAsTheSchedulerStarts(t *testing.T) {
	s := newTestScheduler()
	ran := make(chan struct{}, 4)
	s.Register(Task{Name: "on-start", Interval: time.Hour, RunAtStart: true,
		Run: func(context.Context) (string, error) {
			ran <- struct{}{}
			return "checked", nil
		}})
	var ticked atomic.Int32
	s.Register(Task{Name: "not-on-start", Interval: time.Hour,
		Run: func(context.Context) (string, error) {
			ticked.Add(1)
			return "", nil
		}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		cancel()
		s.Stop()
		t.Fatal("the task did not run when the scheduler started")
	}
	cancel()
	s.Stop()

	if len(ran) != 0 {
		t.Errorf("it ran %d more times in an hour-long interval", len(ran))
	}
	if ticked.Load() != 0 {
		t.Error("a task not marked to run at start ran at start")
	}
	for _, st := range s.Snapshot() {
		if st.Name != "on-start" {
			continue
		}
		if st.Runs != 1 || st.History[0].Manual {
			t.Errorf("runs = %d, history = %+v: the start run should be one scheduled run", st.Runs, st.History)
		}
		if st.NextRun == nil || time.Until(*st.NextRun) < 50*time.Minute {
			t.Errorf("next run = %v, want an interval after the start run", st.NextRun)
		}
	}
}
