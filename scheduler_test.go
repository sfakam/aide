package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sfathall/aide/agent"
	"github.com/sfathall/aide/tasks"
)

// countCmd writes one line to countFile each time it runs, so tests can
// observe how many times a supervised process has been (re)started without
// depending on log output or PIDs.
func countCmd(t *testing.T, countFile string) string {
	t.Helper()
	return "echo x >> " + countFile
}

func readCount(t *testing.T, countFile string) int {
	t.Helper()
	b, err := os.ReadFile(countFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read count file: %v", err)
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func waitForCount(t *testing.T, countFile string, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if readCount(t, countFile) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("count file %q did not reach %d within %s (got %d)", countFile, want, within, readCount(t, countFile))
}

func baseSupervisedTask(t *testing.T, countFile string) tasks.Task {
	t.Helper()
	return tasks.Task{
		ID:                    "watcher",
		Name:                  "Watcher",
		Type:                  "agent",
		AgentMode:             "supervised",
		Enabled:               true,
		RestartBackoffSecs:    1,
		MaxRestartBackoffSecs: 1,
		Cmd:                   &tasks.Script{Name: "watcher", Cmd: countCmd(t, countFile)},
	}
}

func TestScheduler_SupervisedAgent_startsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	if err := sched.add(baseSupervisedTask(t, countFile)); err != nil {
		t.Fatalf("add: %v", err)
	}
	defer sched.stop()

	waitForCount(t, countFile, 1, 2*time.Second)
}

func TestScheduler_SupervisedAgent_restartsOnExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	if err := sched.add(baseSupervisedTask(t, countFile)); err != nil {
		t.Fatalf("add: %v", err)
	}
	defer sched.stop()

	// The command exits immediately every time, so with a 1s backoff we
	// expect at least 2 runs within ~2.5s.
	waitForCount(t, countFile, 2, 3*time.Second)
}

func TestScheduler_Stop_haltsSupervisedProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	sched.add(baseSupervisedTask(t, countFile))
	waitForCount(t, countFile, 1, 2*time.Second)

	sched.stop()
	countAtStop := readCount(t, countFile)
	time.Sleep(1500 * time.Millisecond) // longer than the 1s backoff
	if got := readCount(t, countFile); got != countAtStop {
		t.Errorf("expected no further runs after stop, count went from %d to %d", countAtStop, got)
	}
}

func TestScheduler_Reload_unchangedSupervisedKeepsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	task := baseSupervisedTask(t, countFile)
	task.RestartBackoffSecs = 60 // long enough that a restart within the test window would be obviously wrong
	task.MaxRestartBackoffSecs = 60
	sched.add(task)
	defer sched.stop()

	waitForCount(t, countFile, 1, 2*time.Second)
	before := sched.supervised["watcher"]

	sched.reload([]tasks.Task{task})

	after := sched.supervised["watcher"]
	if before != after {
		t.Error("expected the unchanged supervised task to keep running (same supervisedAgent), got a new one")
	}
}

func TestScheduler_Reload_changedSupervisedRestarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	task := baseSupervisedTask(t, countFile)
	task.RestartBackoffSecs = 60
	task.MaxRestartBackoffSecs = 60
	sched.add(task)
	defer sched.stop()

	waitForCount(t, countFile, 1, 2*time.Second)

	changed := task
	changed.Name = "Watcher v2" // any field change should be treated as "changed"
	sched.reload([]tasks.Task{changed})

	waitForCount(t, countFile, 2, 2*time.Second)
}

func TestScheduler_Reload_removedSupervisedStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sched := newScheduler(ctx, nil, nil, agent.NewHeartbeatTracker())
	task := baseSupervisedTask(t, countFile)
	sched.add(task)
	defer sched.stop()

	waitForCount(t, countFile, 1, 2*time.Second)
	sched.reload(nil)

	if len(sched.supervised) != 0 {
		t.Errorf("expected no supervised tasks after removal, got %d", len(sched.supervised))
	}
}

func TestScheduler_HeartbeatTimeout_restartsStuckProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	hb := agent.NewHeartbeatTracker()
	sched := newScheduler(ctx, nil, nil, hb)
	task := baseSupervisedTask(t, countFile)
	task.AgentID = "watcher-agent"
	task.HeartbeatTimeoutSecs = 1
	// Sleep well past the heartbeat timeout so the process is "alive" (from
	// the OS's point of view) but never reports healthy — the watchdog
	// should kill and restart it well before the sleep would finish.
	task.Cmd = &tasks.Script{Name: "stuck", Cmd: countCmd(t, countFile) + "; sleep 30"}
	sched.add(task)
	defer sched.stop()

	waitForCount(t, countFile, 2, 4*time.Second)
}

func TestScheduler_HeartbeatTimeout_doesNotRestartHealthyProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	hb := agent.NewHeartbeatTracker()
	sched := newScheduler(ctx, nil, nil, hb)
	task := baseSupervisedTask(t, countFile)
	task.AgentID = "watcher-agent"
	task.HeartbeatTimeoutSecs = 1
	task.Cmd = &tasks.Script{Name: "healthy", Cmd: countCmd(t, countFile) + "; sleep 5"}
	sched.add(task)
	defer sched.stop()

	waitForCount(t, countFile, 1, 2*time.Second)

	// Simulate the process calling POST /heartbeat every 300ms, well inside
	// the 1s timeout.
	stopHeartbeats := make(chan struct{})
	defer close(stopHeartbeats)
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeats:
				return
			case <-ticker.C:
				hb.Touch("watcher-agent")
			}
		}
	}()

	time.Sleep(2 * time.Second)
	if got := readCount(t, countFile); got != 1 {
		t.Errorf("expected no restart while heartbeats keep arriving, got count=%d", got)
	}
}
