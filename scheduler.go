package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/sfathall/aide/agent"
	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/tasks"
)

type taskSender interface {
	Send(ctx context.Context, key, sender, text string, statusFn func(string)) (string, error)
}

// supervisedAgent tracks one running "agent_mode: supervised" task so reload
// can diff against it and stop can tear it down cleanly.
type supervisedAgent struct {
	task   tasks.Task
	cancel context.CancelFunc
	done   chan struct{}
}

type scheduler struct {
	mu         sync.Mutex
	c          *cron.Cron
	manager    taskSender
	chs        map[string]channels.Channel
	hb         *agent.HeartbeatTracker
	log        *slog.Logger
	ctx        context.Context // root context for supervised agent processes
	supervised map[string]*supervisedAgent
}

func newScheduler(ctx context.Context, manager taskSender, chs map[string]channels.Channel, hb *agent.HeartbeatTracker) *scheduler {
	return &scheduler{
		c:          cron.New(),
		manager:    manager,
		chs:        chs,
		hb:         hb,
		log:        slog.With("component", "scheduler"),
		ctx:        ctx,
		supervised: make(map[string]*supervisedAgent),
	}
}

func (s *scheduler) add(t tasks.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Type == "agent" && t.AgentMode == "supervised" {
		s.startSupervisedLocked(t)
		return nil
	}
	_, err := s.c.AddFunc(t.Schedule, func() { s.run(t) })
	return err
}

func (s *scheduler) start() {
	s.mu.Lock()
	s.c.Start()
	s.mu.Unlock()
}

// stop halts cron and every supervised agent process, waiting for each to exit.
func (s *scheduler) stop() {
	s.mu.Lock()
	s.c.Stop()
	dones := make([]chan struct{}, 0, len(s.supervised))
	for id, sa := range s.supervised {
		sa.cancel()
		dones = append(dones, sa.done)
		delete(s.supervised, id)
	}
	s.mu.Unlock()
	for _, done := range dones {
		<-done
	}
}

// reload swaps in a new set of enabled tasks without restarting the process.
// Cron-driven tasks (ai/script/agent_mode:scheduled) get a full rebuild, same
// as before. Supervised agent processes are diffed by task id: removed or
// changed ones are stopped, new ones are started, unchanged ones keep running
// uninterrupted (a process restart would be disruptive for no reason).
func (s *scheduler) reload(enabled []tasks.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.c.Stop()
	s.c = cron.New()
	newSupervised := make(map[string]tasks.Task)
	cronCount := 0
	for _, t := range enabled {
		t := t
		if t.Type == "agent" && t.AgentMode == "supervised" {
			newSupervised[t.ID] = t
			continue
		}
		if _, err := s.c.AddFunc(t.Schedule, func() { s.run(t) }); err != nil {
			s.log.Error("failed to re-schedule task after reload", "task", t.ID, "err", err)
		} else {
			s.log.Info("task re-scheduled", "task", t.ID, "name", t.Name, "schedule", t.Schedule)
			cronCount++
		}
	}
	s.c.Start()

	for id, sa := range s.supervised {
		newT, stillWanted := newSupervised[id]
		if !stillWanted || !reflect.DeepEqual(sa.task, newT) {
			s.log.Info("stopping supervised agent (removed or changed)", "task", id)
			sa.cancel()
			<-sa.done
			delete(s.supervised, id)
		}
	}
	for id, t := range newSupervised {
		if _, running := s.supervised[id]; !running {
			s.startSupervisedLocked(t)
		}
	}

	s.log.Info("tasks hot-reloaded", "count", len(enabled), "cron", cronCount, "supervised", len(newSupervised))
}

// startSupervisedLocked spawns the supervisor goroutine for t. Callers must
// hold s.mu.
func (s *scheduler) startSupervisedLocked(t tasks.Task) {
	s.log.Info("starting supervised agent", "task", t.ID, "name", t.Name)
	ctx, cancel := context.WithCancel(s.ctx)
	sa := &supervisedAgent{task: t, cancel: cancel, done: make(chan struct{})}
	s.supervised[t.ID] = sa
	go s.runSupervised(ctx, t, sa.done)
}

func (s *scheduler) run(t tasks.Task) {
	timeout := time.Duration(t.TimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s.log.Info("task starting", "task", t.ID, "name", t.Name, "type", t.Type, "timeout", timeout)
	s.sendCardToOutputs(ctx, t, taskStartCard(t), "⏳ Running task: "+t.Name+"…")

	switch {
	case t.Type == "agent":
		s.runAgentScheduledTask(ctx, t, timeout)
	case t.Type == "script":
		s.runScriptTask(ctx, t, timeout)
	default: // "ai"
		s.runAITask(ctx, t, timeout)
	}
}

func (s *scheduler) runAITask(ctx context.Context, t tasks.Task, timeout time.Duration) {
	if t.PreScript != nil {
		if err := s.runScript(ctx, t, t.PreScript, "pre-script"); err != nil {
			return
		}
	}

	statusFn := func(status string) { s.sendToOutputs(ctx, t, status) }
	start := time.Now()
	resp, err := s.manager.Send(ctx, t.SessionKey(), "scheduler", t.Prompt, statusFn)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer notifyCancel()
		if ctx.Err() == context.DeadlineExceeded {
			s.log.Error("task timed out", "task", t.ID, "timeout", timeout, "elapsed", elapsed)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "⚠️", "Task timed out after "+timeout.String()+"."), "⚠️ Task "+t.Name+" timed out after "+timeout.String()+".")
		} else {
			s.log.Error("task failed", "task", t.ID, "elapsed", elapsed, "err", err)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "❌", err.Error()), "❌ Task "+t.Name+" failed: "+err.Error())
		}
		return
	}
	s.log.Info("task complete", "task", t.ID, "elapsed", elapsed, "resp_len", len(resp))
	s.sendToOutputs(ctx, t, resp) // AI response stays as markdown

	if t.PostScript != nil {
		s.runScript(ctx, t, t.PostScript, "post-script") //nolint:errcheck
	}
}

func (s *scheduler) runScriptTask(ctx context.Context, t tasks.Task, timeout time.Duration) {
	if t.PreScript != nil {
		if err := s.runScript(ctx, t, t.PreScript, "pre-script"); err != nil {
			return
		}
	}

	sc := t.Cmd
	s.log.Info("running main command", "task", t.ID, "name", sc.Name)
	s.sendToOutputs(ctx, t, "⚙️ Executing: **\""+sc.Name+"\"**…")

	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", sc.Cmd)
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer notifyCancel()
		if ctx.Err() == context.DeadlineExceeded {
			s.log.Error("task timed out", "task", t.ID, "timeout", timeout, "elapsed", elapsed)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "⚠️", "Task timed out after "+timeout.String()+"."), "⚠️ Task "+t.Name+" timed out after "+timeout.String()+".")
		} else {
			s.log.Error("task command failed", "task", t.ID, "name", sc.Name, "elapsed", elapsed, "err", err, "output", string(out))
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "❌", "\""+sc.Name+"\" failed: "+err.Error()), "❌ \""+sc.Name+"\" failed: "+err.Error())
		}
		return
	}
	s.log.Info("task command complete", "task", t.ID, "elapsed", elapsed)

	output := strings.TrimSpace(string(out))
	s.sendCardToOutputs(ctx, t, scriptResultCard(t, sc.Name, output, elapsed), "✅ "+t.Name+" completed.")

	if t.PostScript != nil {
		s.runScript(ctx, t, t.PostScript, "post-script") //nolint:errcheck
	}
}

// runAgentScheduledTask runs an "agent_mode: scheduled" task to completion.
// Unlike runScriptTask, stdout/stderr stream live into aide's own log tagged
// with the agent's task id as they're produced, rather than being captured
// and posted as a single block afterward. Outputs (if configured) still get
// a completion/error notification, same as type: script.
func (s *scheduler) runAgentScheduledTask(ctx context.Context, t tasks.Task, timeout time.Duration) {
	sc := t.Cmd
	log := s.log.With("agent", t.ID)
	log.Info("running scheduled agent", "name", sc.Name)
	s.sendToOutputs(ctx, t, "⚙️ Running agent: **\""+sc.Name+"\"**…")

	start := time.Now()
	err := s.runAgentProcess(ctx, t, log)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer notifyCancel()
		if ctx.Err() == context.DeadlineExceeded {
			log.Error("scheduled agent timed out", "timeout", timeout, "elapsed", elapsed)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "⚠️", "Agent timed out after "+timeout.String()+"."), "⚠️ Task "+t.Name+" timed out after "+timeout.String()+".")
		} else {
			log.Error("scheduled agent failed", "elapsed", elapsed, "err", err)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "❌", "\""+sc.Name+"\" failed: "+err.Error()), "❌ \""+sc.Name+"\" failed: "+err.Error())
		}
		return
	}
	log.Info("scheduled agent complete", "elapsed", elapsed)
	if len(t.Outputs) > 0 {
		s.sendToOutputs(ctx, t, "✅ "+t.Name+" completed in "+elapsed.String()+".")
	}
}

// runAgentProcess runs t.Cmd once, streaming its output into log line by
// line as it's produced — this is what both agent_mode: scheduled and
// agent_mode: supervised use to get an agent process's output into aide's
// own log, tagged with its task id, instead of only ever reaching a channel.
// A line carrying its own slog-style "level=" field (as aide's own logging
// and the pr-review agent produce) is logged at that level; otherwise stdout
// defaults to Info and stderr to Warn. See streamLines/slogLevelToken.
//
// It deliberately does not use exec.CommandContext's built-in cancellation:
// that only kills the direct child (the "sh" wrapper), so a grandchild the
// script spawned (e.g. a background `sleep`, or the agent's own re-exec)
// would be orphaned holding the stdout/stderr pipes open, hanging the log
// streamers — and this function — indefinitely even after cancellation.
// Instead the command runs in its own process group, and ctx cancellation
// kills the whole group.
func (s *scheduler) runAgentProcess(ctx context.Context, t tasks.Task, log *slog.Logger) error {
	cmd := exec.Command("sh", "-c", t.Cmd.Cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // negative pid = whole process group
		case <-stopped:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go streamLines(&wg, stdout, log, log.Info)
	go streamLines(&wg, stderr, log, log.Warn)
	wg.Wait()

	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// streamLines copies lines from r into log as they arrive, so a long-running
// process's output reaches aide's log in near real time rather than only
// after it exits. fallback picks the level when a line doesn't carry one of
// its own (e.g. (*slog.Logger).Info for stdout, .Warn for stderr) — see
// slogLevelToken for when a line's own level is used instead.
func streamLines(wg *sync.WaitGroup, r io.Reader, log *slog.Logger, fallback func(msg string, args ...any)) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		logLine := fallback
		if fn, ok := levelLogFunc(log, slogLevelToken(line)); ok {
			logLine = fn
		}
		logLine(line)
	}
}

// slogLevelToken extracts the value of a "level=" field from a line, e.g.
// "INFO" from `time=... level=INFO msg="poll complete" ...` — the format
// Go's slog TextHandler produces, which is what aide's own logging and the
// pr-review agent (and any Go aide-agent following the same convention) use.
// Returns "" if the line has no such field.
func slogLevelToken(line string) string {
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, "level="); ok {
			return v
		}
	}
	return ""
}

// levelLogFunc maps a level token (case-insensitive) to the matching method
// on log, so an agent's own INFO/WARN/ERROR/DEBUG line is logged at aide's
// matching level instead of being flattened to whichever stream it came
// from. ok is false for an unrecognized or empty token, in which case the
// caller's stdout/stderr-based fallback applies.
func levelLogFunc(log *slog.Logger, token string) (fn func(msg string, args ...any), ok bool) {
	switch strings.ToUpper(token) {
	case "DEBUG":
		return log.Debug, true
	case "INFO":
		return log.Info, true
	case "WARN", "WARNING":
		return log.Warn, true
	case "ERROR":
		return log.Error, true
	default:
		return nil, false
	}
}

// runSupervised keeps t.Cmd running indefinitely: on exit (crash or clean),
// it waits an exponentially increasing backoff (capped at
// t.MaxRestartBackoffSecs) and restarts, until ctx is cancelled (task removed
// from config, or aide shutting down).
func (s *scheduler) runSupervised(ctx context.Context, t tasks.Task, done chan<- struct{}) {
	defer close(done)
	log := s.log.With("agent", t.ID)
	backoff := time.Duration(t.RestartBackoffSecs) * time.Second
	maxBackoff := time.Duration(t.MaxRestartBackoffSecs) * time.Second

	for ctx.Err() == nil {
		if s.hb != nil && t.AgentID != "" {
			s.hb.Forget(t.AgentID) // a stale heartbeat from a prior run must not mask this run never sending one
		}

		procCtx, procCancel := context.WithCancel(ctx)
		var watchdogDone chan struct{}
		if s.hb != nil && t.AgentID != "" && t.HeartbeatTimeoutSecs > 0 {
			watchdogDone = make(chan struct{})
			go s.watchHeartbeat(procCtx, procCancel, t, log, watchdogDone)
		}

		start := time.Now()
		err := s.runAgentProcess(procCtx, t, log)
		elapsed := time.Since(start)
		procCancel()
		if watchdogDone != nil {
			<-watchdogDone
		}

		if ctx.Err() != nil {
			return // shutting down or task removed/changed during reload
		}
		if err != nil {
			log.Error("supervised agent exited", "err", err, "elapsed", elapsed)
		} else {
			log.Warn("supervised agent exited cleanly (will be restarted)", "elapsed", elapsed)
		}
		if len(t.Outputs) > 0 {
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
			s.sendCardToOutputs(notifyCtx, t, taskErrorCard(t, "⚠️", t.Name+" exited, restarting in "+backoff.String()+"."), "⚠️ "+t.Name+" exited, restarting in "+backoff.String()+".")
			notifyCancel()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// watchHeartbeat kills the process (via cancel) if t.AgentID hasn't reported
// a heartbeat within t.HeartbeatTimeoutSecs — a process can be alive at the
// OS level but stuck, which plain process-exit detection can't catch.
func (s *scheduler) watchHeartbeat(ctx context.Context, cancel context.CancelFunc, t tasks.Task, log *slog.Logger, done chan<- struct{}) {
	defer close(done)
	timeout := time.Duration(t.HeartbeatTimeoutSecs) * time.Second
	checkEvery := timeout / 2
	if checkEvery < time.Second {
		checkEvery = time.Second
	}
	// Grace period: the process hasn't had a chance to send its first
	// heartbeat yet, so start the clock from when we began watching it.
	deadline := time.Now().Add(timeout)

	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if last, ok := s.hb.LastSeen(t.AgentID); ok {
				deadline = last.Add(timeout)
			}
			if time.Now().After(deadline) {
				log.Error("heartbeat timeout, restarting agent", "agent_id", t.AgentID, "timeout", timeout)
				cancel()
				return
			}
		}
	}
}

// runScript executes a named Script (pre or post phase), announces it on outputs,
// and returns any error. For pre-script failures the message notes that the main
// action will not run; non-empty stdout is forwarded to outputs on success.
func (s *scheduler) runScript(ctx context.Context, t tasks.Task, sc *tasks.Script, phase string) error {
	s.log.Info("running script", "task", t.ID, "phase", phase, "name", sc.Name)
	s.sendToOutputs(ctx, t, "⚙️ Executing "+phase+": **\""+sc.Name+"\"**…")

	cmd := exec.CommandContext(ctx, "sh", "-c", sc.Cmd)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		abortNote := ""
		if phase == "pre-script" {
			abortNote = " — task will not run."
		}
		s.log.Error("script failed", "task", t.ID, "phase", phase, "name", sc.Name, "err", err, "output", output)
		s.sendCardToOutputs(ctx, t,
			scriptPhaseErrorCard(t, sc.Name, phase, err.Error()+abortNote, output),
			"❌ "+phase+" \""+sc.Name+"\" failed: "+err.Error()+abortNote,
		)
		return err
	}
	s.log.Debug("script succeeded", "task", t.ID, "phase", phase, "name", sc.Name, "output", output)
	if output != "" {
		s.sendToOutputs(ctx, t, "```\n"+output+"\n```")
	}
	return nil
}

func (s *scheduler) sendToOutputs(ctx context.Context, t tasks.Task, text string) {
	for _, out := range t.Outputs {
		ch, ok := s.chs[out.ChannelID]
		if !ok {
			s.log.Error("task output channel not found", "task", t.ID, "channel", out.ChannelID)
			continue
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 15*time.Second)
		err := ch.Send(sendCtx, out.RoomID, text)
		sendCancel()
		if err != nil {
			s.log.Error("task output send failed", "task", t.ID, "channel", out.ChannelID, "err", err)
		} else {
			s.log.Info("task output sent", "task", t.ID, "channel", out.ChannelID)
		}
	}
}

// sendCardToOutputs sends an Adaptive Card to all task outputs.
// Channels that implement CardChannel receive the card; others receive the fallback text.
func (s *scheduler) sendCardToOutputs(ctx context.Context, t tasks.Task, card channels.AdaptiveCard, fallback string) {
	for _, out := range t.Outputs {
		ch, ok := s.chs[out.ChannelID]
		if !ok {
			s.log.Error("task output channel not found", "task", t.ID, "channel", out.ChannelID)
			continue
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		if cc, ok := ch.(channels.CardChannel); ok {
			err = cc.SendCard(sendCtx, out.RoomID, card, fallback)
		} else {
			err = ch.Send(sendCtx, out.RoomID, fallback)
		}
		sendCancel()
		if err != nil {
			s.log.Error("task output send failed", "task", t.ID, "channel", out.ChannelID, "err", err)
		} else {
			s.log.Info("task output sent", "task", t.ID, "channel", out.ChannelID)
		}
	}
}

// ─── card builders ────────────────────────────────────────────────────────────

func taskStartCard(t tasks.Task) channels.AdaptiveCard {
	typeLabel := map[string]string{"script": "Script", "ai": "AI", "agent": "Agent"}[t.Type]
	if typeLabel == "" {
		typeLabel = "AI"
	}
	return channels.NewCard(
		channels.TextBlock("⏳ "+t.Name, map[string]any{"weight": "Bolder", "size": "Large"}),
		channels.TextBlock("Running "+typeLabel+" task…", map[string]any{"isSubtle": true, "spacing": "None"}),
	)
}

func taskErrorCard(t tasks.Task, icon, msg string) channels.AdaptiveCard {
	return channels.NewCard(
		channels.TextBlock(icon+" "+t.Name, map[string]any{"weight": "Bolder", "size": "Large", "color": "Attention"}),
		channels.TextBlock(msg, map[string]any{"wrap": true}),
	)
}

// scriptPhaseErrorCard builds a card for a failed pre/post script.
func scriptPhaseErrorCard(t tasks.Task, scriptName, phase, errMsg, output string) channels.AdaptiveCard {
	body := []map[string]any{
		channels.TextBlock("❌ "+t.Name+" — "+phase+" failed", map[string]any{"weight": "Bolder", "size": "Large", "color": "Attention"}),
		channels.FactSet(
			channels.Fact{Title: "Script", Value: scriptName},
			channels.Fact{Title: "Error", Value: errMsg},
		),
	}
	if output != "" {
		const maxOut = 300
		if len(output) > maxOut {
			output = output[:maxOut] + "…"
		}
		body = append(body, channels.TextBlock(output, map[string]any{"fontType": "Monospace", "size": "Small", "spacing": "Medium"}))
	}
	return channels.NewCard(body...)
}

// scriptResultCard builds a card for a completed type:script task.
// If output is longer than 500 chars it is truncated to avoid overwhelming the card.
func scriptResultCard(t tasks.Task, cmdName, output string, elapsed time.Duration) channels.AdaptiveCard {
	const maxOutput = 500
	body := []map[string]any{
		channels.TextBlock("✅ "+t.Name, map[string]any{"weight": "Bolder", "size": "Large", "color": "Good"}),
		channels.FactSet(
			channels.Fact{Title: "Command", Value: cmdName},
			channels.Fact{Title: "Elapsed", Value: elapsed.Round(time.Millisecond).String()},
		),
	}
	if output != "" {
		if len(output) > maxOutput {
			output = output[:maxOutput] + "…"
		}
		body = append(body, channels.TextBlock(output, map[string]any{"fontType": "Monospace", "size": "Small", "spacing": "Medium"}))
	}
	return channels.NewCard(body...)
}
