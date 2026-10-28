// Package runner owns one explicitly approved interactive root process.
// It never captures its output, mutates the parent environment, or kills a group.
package runner

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

type View struct {
	State                string `json:"state"`
	EnvironmentDelivered bool   `json:"environment_delivered"`
	ExitCode             *int   `json:"exit_code,omitempty"`
	TerminalRestored     bool   `json:"terminal_restored"`
	RootOnly             bool   `json:"root_only"`
	ProgramStopAllowed   bool   `json:"program_stop_allowed"`
	StopReason           string `json:"stop_reason,omitempty"`
	ArtifactRetained     bool   `json:"artifact_retained,omitempty"`
	CleanupCode          string `json:"cleanup_code,omitempty"`
}
type Runner struct {
	mu                            sync.Mutex
	files                         [3]*os.File
	active, disabled, hasLaunched bool
}

func New(in, out, errOut *os.File) *Runner { return &Runner{files: [3]*os.File{in, out, errOut}} }
func (r *Runner) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled || r.active || !nativeAvailable() || !reapingPolicySafe() {
		return false
	}
	_, e := captureTerminal(r.files)
	return e == nil
}
func (r *Runner) Active() bool         { r.mu.Lock(); defer r.mu.Unlock(); return r.active }
func (r *Runner) OwnsInterrupts() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.hasLaunched }

type Process struct {
	mu                    sync.Mutex
	pid                   int
	reaped, exitConfirmed bool
	view                  View
	done                  chan struct{}
	poll                  func(int) (bool, int, error)
	signal                func(int, syscall.Signal) error
	signalPolicy          func() bool
	prepared              *Prepared
}

// The native spawn uses the prepared private entry and the held directory FD.
// No Cmd.Wait or other reaper may ever wait for this PID.
func (r *Runner) Start(prepared *Prepared, env []string) (*Process, error) {
	// Defense at the final pre-dyld boundary, including direct adapter callers.
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYLD_") {
			return nil, errors.New("loader_environment_unsupported")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active || r.disabled || prepared == nil || !reapingPolicySafe() {
		return nil, errors.New("terminal_or_supervisor_unavailable")
	}
	state, e := captureTerminal(r.files)
	if e != nil {
		return nil, errors.New("interactive_terminal_required")
	}
	pid, e := spawnApproved(prepared, r.files, env)
	if e != nil {
		return nil, errors.New("native_start_failed")
	}
	p := &Process{pid: pid, prepared: prepared, done: make(chan struct{}), poll: pollOwnedChild, signal: syscall.Kill, signalPolicy: reapingPolicySafe, view: View{State: "running", EnvironmentDelivered: true, RootOnly: true, ProgramStopAllowed: true}}
	r.active = true
	r.hasLaunched = true
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			p.mu.Lock()
			ended := p.pollLocked()
			confirmed := p.exitConfirmed
			p.mu.Unlock()
			if ended {
				restored := confirmed && restoreTerminal(r.files, state) == nil
				retained := true
				if confirmed {
					retained = prepared.Close() != nil
				}
				p.mu.Lock()
				p.view.TerminalRestored = restored
				p.view.ArtifactRetained = retained
				p.view.CleanupCode = prepared.CleanupCode()
				p.mu.Unlock()
				r.mu.Lock()
				r.active = false
				r.disabled = !restored
				r.mu.Unlock()
				close(p.done)
				return
			}
			<-ticker.C
		}
	}()
	return p, nil
}

func pollOwnedChild(pid int) (bool, int, error) {
	var status syscall.WaitStatus
	got, e := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if e == syscall.EINTR {
		return false, 0, nil
	}
	if e != nil {
		return false, 0, e
	}
	if got == 0 {
		return false, 0, nil
	}
	if got != pid {
		return false, 0, errors.New("unexpected_child")
	}
	if status.Exited() {
		return true, status.ExitStatus(), nil
	}
	if status.Signaled() {
		return true, -1, nil
	}
	return false, 0, nil
}

// Reaping and signalling are serialized under this lock. Until this sole
// reaper collects the child, its PID cannot be reused; after collection the
// terminal state is published before releasing the lock and signals are refused.
func (p *Process) pollLocked() bool {
	if p.reaped {
		return true
	}
	exited, code, e := p.poll(p.pid)
	if e != nil {
		p.reaped = true
		p.view.State = "exit_unconfirmed"
		p.view.ProgramStopAllowed = false
		p.view.StopReason = "process_ownership_unconfirmed"
		return true
	}
	if !exited {
		return false
	}
	p.reaped = true
	p.exitConfirmed = true
	p.view.ExitCode = &code
	p.view.ProgramStopAllowed = false
	if p.view.State == "stop_requested" {
		p.view.State = "stopped"
	} else {
		p.view.State = "exited"
	}
	return true
}
func (p *Process) View() View {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.view
	if v.ExitCode != nil {
		code := *v.ExitCode
		v.ExitCode = &code
	}
	return v
}
func (p *Process) Done() <-chan struct{} { return p.done }

// Recovery is limited to this already-exited root's own temporary artifact.
func (p *Process) RetryCleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exitConfirmed || p.prepared == nil {
		return errors.New("exit_unconfirmed")
	}
	e := p.prepared.Close()
	p.view.ArtifactRetained = e != nil
	p.view.CleanupCode = p.prepared.CleanupCode()
	return e
}
func (p *Process) signalLocked(sig syscall.Signal) bool {
	if p.pollLocked() {
		return false
	}
	if p.pid <= 0 || !p.signalPolicy() {
		p.view.ProgramStopAllowed = false
		p.view.StopReason = "automatic_reaping_or_policy_unknown"
		return false
	}
	if e := p.signal(p.pid, sig); e != nil && e != syscall.ESRCH {
		p.view.ProgramStopAllowed = false
		p.view.StopReason = "root_signal_failed"
		return false
	}
	return true
}
func (p *Process) Stop() {
	p.mu.Lock()
	if p.reaped || p.view.State != "running" {
		p.mu.Unlock()
		return
	}
	sent := p.signalLocked(syscall.SIGTERM)
	if sent {
		p.view.State = "stop_requested"
	}
	p.mu.Unlock()
	if !sent {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-p.done:
			return
		case <-timer.C:
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.view.State == "stop_requested" {
			p.signalLocked(syscall.SIGKILL)
		}
	}()
}
