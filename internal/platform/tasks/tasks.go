// Package tasks is the scheduled-task framework.
//
// Requirements §9: "Scheduled task framework with visible next-run/last-run,
// manual trigger, and failure history."
//
// Three properties do the work:
//
//   - A task never runs concurrently with itself. Overlapping runs of a purge
//     or a scan corrupt each other's assumptions, and a slow task would
//     otherwise pile up runs until something falls over.
//   - A panic inside a task is contained and recorded as a failure. One bad
//     task must not take the process down.
//   - Every outcome is visible: last run, next run, duration, the error text,
//     and a bounded history. A scheduled job you cannot see is a scheduled job
//     you will not notice has been failing for a month.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// historyDepth is how many outcomes are kept per task. Bounded so a task on a
// short interval cannot grow memory without limit.
const historyDepth = 20

// Runner performs a task and returns a one-line summary of what it did.
type Runner func(ctx context.Context) (summary string, err error)

// Task is a registered unit of scheduled work.
type Task struct {
	Name        string
	Description string
	// Interval between runs. Zero means the task only runs when triggered.
	Interval time.Duration
	// RunAtStart also runs the task once when the scheduler starts, rather
	// than first at start + Interval.
	//
	// For a task whose real schedule is kept somewhere a restart does not
	// reset — the backup check's is the age of the newest backup on disk. The
	// ticker restarts with the process, so without this an instance restarted
	// more often than Interval would never run the task at all.
	RunAtStart bool
	Run        Runner
}

// Outcome is one completed run.
type Outcome struct {
	StartedAt time.Time
	Duration  time.Duration
	Summary   string
	Err       string
	Manual    bool
}

// Succeeded reports whether the run completed without error.
func (o Outcome) Succeeded() bool { return o.Err == "" }

// Status is a task's public state.
type Status struct {
	Name        string
	Description string
	Interval    time.Duration
	Running     bool
	LastRun     *time.Time
	NextRun     *time.Time
	Runs        int
	Failures    int
	History     []Outcome
}

type taskState struct {
	task     Task
	mu       sync.Mutex
	running  bool
	lastRun  time.Time
	nextRun  time.Time
	runs     int
	failures int
	history  []Outcome
}

// Errors returned by the scheduler.
var (
	ErrUnknownTask    = fmt.Errorf("tasks: no such task")
	ErrAlreadyRunning = fmt.Errorf("tasks: task is already running")
)

// Scheduler owns the registered tasks and their timers.
type Scheduler struct {
	mu     sync.RWMutex
	tasks  map[string]*taskState
	logger *slog.Logger
	now    func() time.Time

	// observer is notified after every run, for metrics.
	observer func(name string, o Outcome)

	started bool
	wg      sync.WaitGroup
}

// New creates a scheduler. now is injectable for tests.
func New(logger *slog.Logger, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{tasks: map[string]*taskState{}, logger: logger, now: now}
}

// Observe installs a callback invoked after each run. Used to feed metrics.
func (s *Scheduler) Observe(f func(name string, o Outcome)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = f
}

// Register adds a task. It panics on a duplicate name or a nil runner, because
// both are programming errors that would otherwise surface as a job silently
// never running.
func (s *Scheduler) Register(t Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t.Name == "" {
		panic("tasks: a task must have a name")
	}
	if t.Run == nil {
		panic("tasks: task " + t.Name + " has no runner")
	}
	if _, dup := s.tasks[t.Name]; dup {
		panic("tasks: task " + t.Name + " is registered twice")
	}
	if s.started {
		panic("tasks: task " + t.Name + " registered after the scheduler started")
	}
	if t.RunAtStart && t.Interval <= 0 {
		// A manual-only task never starts a timer, so this would be ignored.
		panic("tasks: task " + t.Name + " runs at start but has no interval")
	}

	st := &taskState{task: t}
	if t.Interval > 0 {
		st.nextRun = s.now().Add(t.Interval)
	}
	s.tasks[t.Name] = st
}

// Start begins the timers. It returns immediately; Stop waits for in-flight
// runs to finish.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	states := make([]*taskState, 0, len(s.tasks))
	for _, st := range s.tasks {
		if st.task.Interval > 0 {
			states = append(states, st)
		}
	}
	s.mu.Unlock()

	for _, st := range states {
		s.wg.Add(1)
		go func(st *taskState) {
			defer s.wg.Done()
			ticker := time.NewTicker(st.task.Interval)
			defer ticker.Stop()
			if st.task.RunAtStart && ctx.Err() == nil {
				s.execute(ctx, st, false)
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.execute(ctx, st, false)
				}
			}
		}(st)
	}
}

// Stop waits for in-flight runs. The caller cancels the context first.
func (s *Scheduler) Stop() { s.wg.Wait() }

// Trigger runs a task now, out of schedule.
//
// A task already running is refused rather than queued: queueing a manual run
// behind a slow one means an operator clicks the button, sees nothing happen,
// and clicks again.
func (s *Scheduler) Trigger(ctx context.Context, name string) (Outcome, error) {
	s.mu.RLock()
	st, ok := s.tasks[name]
	s.mu.RUnlock()
	if !ok {
		return Outcome{}, ErrUnknownTask
	}

	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return Outcome{}, ErrAlreadyRunning
	}
	st.mu.Unlock()

	return s.execute(ctx, st, true), nil
}

// execute runs one task, recording the outcome.
func (s *Scheduler) execute(ctx context.Context, st *taskState, manual bool) Outcome {
	st.mu.Lock()
	if st.running {
		// A scheduled tick that lands while the previous run is still going is
		// dropped, not queued. Piling up runs of a slow task is how a purge
		// job becomes an outage.
		st.mu.Unlock()
		return Outcome{}
	}
	st.running = true
	started := s.now()
	st.mu.Unlock()

	out := Outcome{StartedAt: started, Manual: manual}

	func() {
		defer func() {
			if p := recover(); p != nil {
				out.Err = fmt.Sprintf("panic: %v", p)
				s.logger.Error("scheduled task panicked",
					slog.String("task", st.task.Name), slog.Any("panic", p))
			}
		}()
		summary, err := st.task.Run(ctx)
		out.Summary = summary
		if err != nil {
			out.Err = err.Error()
		}
	}()

	out.Duration = s.now().Sub(started)

	st.mu.Lock()
	st.running = false
	st.lastRun = started
	st.runs++
	if !out.Succeeded() {
		st.failures++
	}
	if st.task.Interval > 0 {
		st.nextRun = s.now().Add(st.task.Interval)
	}
	st.history = append(st.history, out)
	if len(st.history) > historyDepth {
		st.history = st.history[len(st.history)-historyDepth:]
	}
	st.mu.Unlock()

	s.mu.RLock()
	observer := s.observer
	s.mu.RUnlock()
	if observer != nil {
		observer(st.task.Name, out)
	}

	if out.Succeeded() {
		if out.Summary != "" {
			s.logger.Info("scheduled task",
				slog.String("task", st.task.Name),
				slog.String("summary", out.Summary),
				slog.Duration("duration", out.Duration),
				slog.Bool("manual", manual))
		}
	} else {
		s.logger.Error("scheduled task failed",
			slog.String("task", st.task.Name),
			slog.String("error", out.Err),
			slog.Duration("duration", out.Duration),
			slog.Bool("manual", manual))
	}
	return out
}

// Snapshot returns every task's state, sorted by name.
func (s *Scheduler) Snapshot() []Status {
	s.mu.RLock()
	states := make([]*taskState, 0, len(s.tasks))
	for _, st := range s.tasks {
		states = append(states, st)
	}
	s.mu.RUnlock()

	out := make([]Status, 0, len(states))
	for _, st := range states {
		st.mu.Lock()
		status := Status{
			Name:        st.task.Name,
			Description: st.task.Description,
			Interval:    st.task.Interval,
			Running:     st.running,
			Runs:        st.runs,
			Failures:    st.failures,
			History:     append([]Outcome(nil), st.history...),
		}
		if !st.lastRun.IsZero() {
			t := st.lastRun
			status.LastRun = &t
		}
		if !st.nextRun.IsZero() {
			t := st.nextRun
			status.NextRun = &t
		}
		st.mu.Unlock()
		out = append(out, status)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns the registered task names, sorted.
func (s *Scheduler) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.tasks))
	for name := range s.tasks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
