package execution

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"cc-anti-ban-skill/internal/health"
	"cc-anti-ban-skill/internal/runner"
)

type fakeProcess struct {
	mu   sync.Mutex
	done chan struct{}
	view runner.View
	once sync.Once
}

func (p *fakeProcess) RetryCleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.view.ArtifactRetained = false
	return nil
}
func (p *fakeProcess) View() runner.View     { p.mu.Lock(); defer p.mu.Unlock(); return p.view }
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Stop() {
	p.once.Do(func() {
		p.mu.Lock()
		code := 0
		p.view.State = "stopped"
		p.view.ExitCode = &code
		p.view.TerminalRestored = true
		p.mu.Unlock()
		close(p.done)
	})
}

type environmentFixture struct {
	d             health.LocalIO
	path          string
	now           time.Time
	available     bool
	starts, dials atomic.Int32
	versions      atomic.Int32
	env           []string
	m             *Manager
	process       *fakeProcess
}

func executionFixture(t *testing.T) *environmentFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(root, "project")
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0700)
	_ = os.MkdirAll(dir, 0700)
	f := &environmentFixture{path: filepath.Join(root, "fake-claude"), now: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC), available: true}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	source, e := os.Open(binary)
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(target, source)
	source.Close()
	target.Close()
	if e != nil {
		t.Fatal(e)
	}
	f.d = health.LocalIO{OS: "darwin", Home: home, Directory: dir, Env: map[string]string{}, Lookup: func(string) (string, error) { return f.path, nil }, Version: func(context.Context, string) (string, error) { f.versions.Add(1); return "2.1.999 (Claude Code)", nil }, Stat: func(path string) (fs.FileInfo, error) {
		if strings.HasPrefix(path, root+"/") {
			return os.Lstat(path)
		}
		return nil, fs.ErrNotExist
	}, ReadSettings: func() ([]byte, error) { return os.ReadFile(filepath.Join(home, ".claude", "settings.json")) }, Now: func() time.Time { return f.now }}
	f.m = New(context.Background(), Dependencies{Recheck: func(ctx context.Context, previous health.Snapshot) health.Snapshot {
		return health.Reinspect(ctx, f.d, previous)
	}, Available: func() bool { return f.available }, Start: func(prepared *runner.Prepared, env []string) (Process, error) {
		f.starts.Add(1)
		f.env = append([]string(nil), env...)
		f.process = &fakeProcess{done: make(chan struct{}), view: runner.View{State: "running", EnvironmentDelivered: true, RootOnly: true}}
		return f.process, nil
	}, Environment: func() []string {
		return []string{"FAKE_PARENT_SECRET=fictional", "PATH=/fixed", "https_proxy=http://old.invalid", "NO_PROXY=*", "ALL_PROXY=socks://old.invalid"}
	}, Network: func() health.NetworkIO {
		return health.NetworkIO{Roots: x509.NewCertPool(), Now: func() time.Time { return f.now }, Resolve: func(context.Context, string, string) ([]net.IP, error) {
			t.Error("literal proxy resolved")
			return nil, nil
		}, Dial: func(_ context.Context, network, address string) (net.Conn, error) {
			f.dials.Add(1)
			if network != "tcp" || address != "127.0.0.1:4567" {
				t.Error("wrong approved transport")
			}
			return nil, errors.New("fixture unavailable")
		}}
	}, Now: func() time.Time { return f.now }})
	t.Cleanup(func() {
		f.m.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if !f.m.Wait(ctx) {
			t.Error("fixture operation remained")
		}
	})
	return f
}
func (f *environmentFixture) plan() Plan {
	return f.m.Prepare(health.Inspect(context.Background(), f.d), "127.0.0.1", 4567)
}
func waitStatus(t *testing.T, m *Manager, id string, predicate func(Status) bool) Status {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		s, e := m.Status(id)
		if e == nil && predicate(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("status timeout")
	return Status{}
}

func TestApprovedEnvironmentAndReferenceAreSeparate(t *testing.T) {
	f := executionFixture(t)
	p := f.plan()
	if !p.Allowed || f.starts.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("preview side effect or unavailable")
	}
	if _, e := f.m.Accept(p); e != nil {
		t.Fatal(e)
	}
	s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
	if f.starts.Load() != 1 || f.dials.Load() != 1 || s.Process == nil || !s.Process.EnvironmentDelivered || s.Reference.Score.Value == nil || *s.Reference.Score.Value != 49 || s.Comparable {
		t.Fatalf("wrong split evidence: %+v", s)
	}
	got := map[string]string{}
	for _, v := range f.env {
		k, val, _ := strings.Cut(v, "=")
		got[k] = val
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if got[k] != p.Proxy {
			t.Fatal("proxy not delivered")
		}
	}
	if got["FAKE_PARENT_SECRET"] != "fictional" || got["PATH"] != "/fixed" || got["NO_PROXY"] != "" || got["ALL_PROXY"] != "" {
		t.Fatal("wrong child overlay")
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "fictional") || strings.Contains(string(raw), f.path) || strings.Contains(string(raw), p.Proxy) {
		t.Fatal("status exposed private data")
	}
	_, _ = f.m.Stop(p.ID)
	waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
	if _, e := f.m.Accept(p); e == nil || f.starts.Load() != 1 {
		t.Fatal("consumed plan replayed")
	}
}

func TestPlanAndRevalidationBlockUnapprovedStartup(t *testing.T) {
	for _, name := range []string{"no tty", "expired", "CLI replaced", "settings changed", "provider", "invalid settings", "user proxy conflict", "host managed"} {
		t.Run(name, func(t *testing.T) {
			f := executionFixture(t)
			switch name {
			case "no tty":
				f.available = false
			case "provider":
				f.d.Env["CLAUDE_CODE_USE_ANTHROPIC_AWS"] = "0"
			case "host managed":
				f.d.Env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"] = "present"
			case "invalid settings":
				_ = os.WriteFile(filepath.Join(f.d.Home, ".claude", "settings.json"), []byte("{"), 0600)
			case "user proxy conflict":
				_ = os.WriteFile(filepath.Join(f.d.Home, ".claude", "settings.json"), []byte(`{"env":{"HTTPS_PROXY":"http://different.invalid"}}`), 0600)
			}
			p := f.plan()
			switch name {
			case "expired":
				f.now = f.now.Add(time.Minute)
			case "CLI replaced":
				_ = os.WriteFile(f.path, []byte("#!/bin/sh\nexit 2\n"), 0700)
			case "settings changed":
				_ = os.WriteFile(filepath.Join(f.d.Home, ".claude", "settings.json"), []byte(`{"unrelated":"fixture"}`), 0600)
			}
			if _, e := f.m.Accept(p); e == nil {
				waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
			}
			if f.starts.Load() != 0 || f.dials.Load() != 0 {
				t.Fatal("unapproved startup")
			}
		})
	}
}

func TestUnknownEffectiveLayersDoNotBlockOrdinaryCandidate(t *testing.T) {
	f := executionFixture(t)
	_ = os.MkdirAll(filepath.Join(f.d.Directory, ".claude"), 0700)
	_ = os.WriteFile(filepath.Join(f.d.Directory, ".claude", "settings.json"), []byte(`{"fixture":"unread"}`), 0600)
	p := f.plan()
	if !p.Allowed {
		t.Fatalf("unknown effective layer permanently blocked: %s", p.Reason)
	}
	if !strings.Contains(strings.Join(p.Warnings, " "), "remote/MDM") {
		t.Fatal("unknown policy concealed")
	}
	for _, candidate := range []struct {
		host string
		port int
	}{{"localhost", 4567}, {"10.0.0.1", 80}, {"127.0.0.1", 0}, {"127.0.0.1", 65536}} {
		if f.m.Prepare(health.Inspect(context.Background(), f.d), candidate.host, candidate.port).Allowed {
			t.Fatal("arbitrary recipient accepted")
		}
	}
}

func TestCancelAcceptedBeforeOSStart(t *testing.T) {
	f := executionFixture(t)
	p := f.plan()
	entered, release := make(chan struct{}), make(chan struct{})
	observe := f.m.deps.Recheck
	f.m.deps.Recheck = func(ctx context.Context, previous health.Snapshot) health.Snapshot {
		close(entered)
		<-release
		return observe(ctx, previous)
	}
	_, e := f.m.Accept(p)
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	if _, e = f.m.Stop("other-operation"); e == nil {
		t.Fatal("foreign stop accepted")
	}
	s, e := f.m.Stop(p.ID)
	if e != nil || s.State != "cancelled_before_start" {
		t.Fatal("queued cancel failed")
	}
	close(release)
	waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
	if f.starts.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("cancelled operation executed")
	}
}

func TestFIFOIsRejectedOrCannotReplaceApprovedFD(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(strconv.FormatBool(after), func(t *testing.T) {
			f := executionFixture(t)
			p := f.plan()
			if e := os.Remove(f.path); e != nil {
				t.Fatal(e)
			}
			if e := syscall.Mkfifo(f.path, 0700); e != nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			go func() {
				if after {
					_, _ = f.m.Accept(p)
				} else {
					p = f.plan()
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("FIFO blocked plan path")
			}
			if after {
				waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
				if f.starts.Load() != 1 || f.dials.Load() != 1 {
					t.Fatal("approved held object did not remain executable")
				}
				_, _ = f.m.Stop(p.ID)
				waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
			} else if p.Allowed {
				t.Fatal("FIFO executable allowed")
			}
			if !after && (f.starts.Load() != 0 || f.dials.Load() != 0) {
				t.Fatal("FIFO replacement executed")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := runner.OpenImage(ctx, "/not-opened", "/not-opened"); e == nil {
		t.Fatal("cancelled binding accepted")
	}
}

func TestFailedReferenceNeverComparableEvenWithSameTrustProfile(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(strconv.FormatBool(same), func(t *testing.T) {
			f := executionFixture(t)
			source := health.Inspect(context.Background(), f.d)
			candidate, reason := source.CandidateReference("http://127.0.0.1:4567", f.now)
			if reason != "" {
				t.Fatal(reason)
			}
			beforeNetwork := f.m.deps.Network()
			beforeNetwork.TrustID = "fixture-trust-before"
			source.Report = candidate.Probe(context.Background(), candidate.Plan(f.now), beforeNetwork)
			originalNetwork := f.m.deps.Network
			f.m.deps.Network = func() health.NetworkIO {
				n := originalNetwork()
				n.TrustID = "fixture-trust-after"
				if same {
					n.TrustID = "fixture-trust-before"
				}
				return n
			}
			p := f.m.Prepare(source, "127.0.0.1", 4567)
			if !p.Allowed {
				t.Fatal(p.Reason)
			}
			if _, e := f.m.Accept(p); e != nil {
				t.Fatal(e)
			}
			s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
			if s.Comparable {
				t.Fatal("trust profile comparison incorrectly promoted")
			}
		})
	}
}

func TestStartCrossingExpiryDoesNotCompleteOrDial(t *testing.T) {
	f := executionFixture(t)
	p := f.plan()
	versions := f.versions.Load()
	start := f.m.deps.Start
	f.m.deps.Start = func(prepared *runner.Prepared, env []string) (Process, error) {
		process, e := start(prepared, env)
		f.now = f.now.Add(61 * time.Second)
		return process, e
	}
	if _, e := f.m.Accept(p); e != nil {
		t.Fatal(e)
	}
	s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
	if s.Process == nil || !s.Process.EnvironmentDelivered || s.ReferenceState != "expired" || s.Comparable || f.starts.Load() != 1 || f.dials.Load() != 0 {
		t.Fatalf("late reference incorrectly executed or promoted: %+v", s)
	}
	if s.Reference.Probe == nil || s.Reference.Probe.StartedAt != nil || s.Reference.Probe.Attempts != (health.ProbeAttempts{}) {
		t.Fatal("unexecuted stages acquired attempts or timestamp")
	}
	if f.versions.Load() != versions {
		t.Fatal("confirmation executed an extra version command")
	}
}

func TestConfirmationRechecksWithoutAnotherCLICommand(t *testing.T) {
	f := executionFixture(t)
	p := f.plan()
	versions := f.versions.Load()
	if _, e := f.m.Accept(p); e != nil {
		t.Fatal(e)
	}
	s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
	if f.versions.Load() != versions || f.starts.Load() != 1 {
		t.Fatal("additional CLI execution during confirmation")
	}
	if s.ReferenceState != "failed" || s.Comparable || s.Reference.Probe.Attempts.TCP != 1 {
		t.Fatal("failed attempted probe was marked complete or comparable")
	}
}
