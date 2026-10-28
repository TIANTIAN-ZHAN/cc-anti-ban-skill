package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cc-anti-ban-skill/internal/runner"
)

func TestCleanupRetainedAcrossEveryPrestartFailure(t *testing.T) {
	for _, mode := range []string{"start", "cancelled", "invalidated", "materialize", "service_expired"} {
		t.Run(mode, func(t *testing.T) {
			f := executionFixture(t)
			p := f.plan()
			second := f.plan()
			defer second.Close()
			var deny atomic.Bool
			deny.Store(true)
			var calls atomic.Int32
			f.m.deps.Cleanup = func(p *runner.Prepared) error {
				calls.Add(1)
				if deny.Load() {
					return errors.New("fixture private-path deliberately omitted")
				}
				return p.Close()
			}
			materialize := f.m.deps.Materialize
			f.m.deps.Materialize = func(ctx context.Context, i *runner.Image) (*runner.Prepared, error) {
				prepared, e := materialize(ctx, i)
				if e != nil {
					return prepared, e
				}
				switch mode {
				case "cancelled":
					f.m.Stop(p.ID)
				case "invalidated":
					f.available = false
				case "materialize":
					return prepared, errors.New("copy failed")
				case "service_expired":
					f.m.Close()
				}
				return prepared, nil
			}
			f.m.deps.Start = func(*runner.Prepared, []string) (Process, error) { return nil, errors.New("start failed") }
			if _, e := f.m.Accept(p); e != nil {
				t.Fatal(e)
			}
			s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
			if !s.ArtifactRetained || !s.CleanupRetryAllowed || !f.m.ResourcesPending() || !f.m.Active() {
				t.Fatal("retained state missing")
			}
			if f.m.Wait(context.Background()) {
				t.Fatal("retained resource reported drained")
			}
			if _, e := f.m.Accept(second); e == nil {
				t.Fatal("second plan overwrote cleanup")
			}
			if q := f.plan(); q.Allowed {
				q.Close()
				t.Fatal("new plan allowed during retained cleanup")
			}
			if _, e := f.m.RetryCleanup("foreign"); e == nil {
				t.Fatal("foreign nonce cleanup")
			}
			s, e := f.m.RetryCleanup(p.ID)
			if e != nil || !s.ArtifactRetained {
				t.Fatal("retry failure discarded")
			}
			raw, _ := json.Marshal(s)
			if strings.Contains(string(raw), f.path) || strings.Contains(string(raw), "private-path") {
				t.Fatal("private data escaped")
			}
			sentinel := filepath.Join(t.TempDir(), "unrelated")
			os.WriteFile(sentinel, []byte("keep"), 0600)
			deny.Store(false)
			before := calls.Load()
			var wg sync.WaitGroup
			for j := 0; j < 8; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s, e := f.m.RetryCleanup(p.ID)
					if e != nil || s.ArtifactRetained {
						t.Error("concurrent retry")
					}
				}()
			}
			wg.Wait()
			if calls.Load() != before+1 || f.m.ResourcesPending() {
				t.Fatal("cleanup repeated or not released")
			}
			if _, e := os.Stat(sentinel); e != nil {
				t.Fatal("unrelated removed")
			}
		})
	}
}

func TestOverlayDropsEveryDYLDNameWithoutChangingInput(t *testing.T) {
	input := []string{"DYLD_PRINT_ENV=1", "DYLD_PRINT_TO_FILE=/fake", "DYLD_FUTURE=private", "FAKE_PARENT_SECRET=fictional", "HTTP_PROXY=old"}
	before := strings.Join(input, "\n")
	out := Overlay(input, "http://127.0.0.1:4567")
	for _, entry := range out {
		if strings.HasPrefix(entry, "DYLD_") {
			t.Fatal("loader reached child")
		}
	}
	if strings.Join(input, "\n") != before || !strings.Contains(strings.Join(out, "\n"), "FAKE_PARENT_SECRET=fictional") {
		t.Fatal("parent or ordinary inheritance changed")
	}
}

type retainedProcess struct {
	*fakeProcess
	artifact *runner.Prepared
	attempts int
}

func (p *retainedProcess) RetryCleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if p.attempts == 1 {
		return errors.New("cleanup still denied")
	}
	e := p.artifact.Close()
	p.view.ArtifactRetained = e != nil
	return e
}
func TestExitedProcessRecoverySurvivesNewPlanAttempts(t *testing.T) {
	f := executionFixture(t)
	p := f.plan()
	var process *retainedProcess
	f.m.deps.Start = func(artifact *runner.Prepared, _ []string) (Process, error) {
		process = &retainedProcess{fakeProcess: &fakeProcess{done: make(chan struct{}), view: runner.View{State: "running", ArtifactRetained: true}}, artifact: artifact}
		return process, nil
	}
	if _, e := f.m.Accept(p); e != nil {
		t.Fatal(e)
	}
	waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Reference != nil })
	f.m.Stop(p.ID)
	s := waitStatus(t, f.m, p.ID, func(s Status) bool { return s.Finished })
	if !s.ArtifactRetained || !s.CleanupRetryAllowed {
		t.Fatal("exited recovery missing")
	}
	if q := f.plan(); q.Allowed {
		q.Close()
		t.Fatal("new plan lost exited process")
	}
	s, _ = f.m.RetryCleanup(p.ID)
	if !s.ArtifactRetained {
		t.Fatal("failed cleanup hidden")
	}
	s, _ = f.m.RetryCleanup(p.ID)
	if s.ArtifactRetained || process.attempts != 2 {
		t.Fatal("process retry not connected")
	}
	q := f.plan()
	defer q.Close()
	if !q.Allowed {
		t.Fatal("successful cleanup did not release new plan")
	}
}
