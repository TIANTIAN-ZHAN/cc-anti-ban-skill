//go:build darwin && cgo

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"cc-anti-ban-skill/internal/runner"
)

func TestManagerRecoversPartiallyCreatedArtifact(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home := t.TempDir()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestManagerPartialArtifactChild$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + home, "CC_PARTIAL_ARTIFACT_CHILD=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated manager recovery: %v %s", err, out)
	}
}

func TestManagerPartialArtifactChild(t *testing.T) {
	if os.Getenv("CC_PARTIAL_ARTIFACT_CHILD") != "1" {
		t.Skip("isolated resource child")
	}
	f := executionFixture(t)
	plan := f.plan()
	if !plan.Allowed {
		t.Fatal(plan.Reason)
	}
	var materializeErr error
	f.m.deps.Materialize = func(ctx context.Context, image *runner.Image) (*runner.Prepared, error) {
		var before syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
			return nil, err
		}
		limit := before
		limit.Cur = 64
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			return nil, err
		}
		var blockers []*os.File
		defer func() {
			syscall.Setrlimit(syscall.RLIMIT_NOFILE, &before)
			for _, file := range blockers {
				if file != nil {
					file.Close()
				}
			}
		}()
		for {
			file, err := os.Open(os.DevNull)
			if errors.Is(err, syscall.EMFILE) {
				break
			}
			if err != nil {
				return nil, err
			}
			blockers = append(blockers, file)
		}
		if len(blockers) < 3 {
			return nil, errors.New("fixture descriptor capacity")
		}
		for i := len(blockers) - 3; i < len(blockers); i++ {
			blockers[i].Close()
			blockers[i] = nil
		}
		prepared, err := image.Materialize(ctx)
		materializeErr = err
		return prepared, err
	}
	// Preserve one retryable cleanup failure after the real allocation failure,
	// so the manager must expose and later release this exact partial artifact.
	var blocked atomic.Bool
	blocked.Store(true)
	f.m.deps.Cleanup = func(p *runner.Prepared) error {
		if blocked.Load() {
			return syscall.EMFILE
		}
		return p.Close()
	}
	if _, err := f.m.Accept(plan); err != nil {
		t.Fatal(err)
	}
	status := waitStatus(t, f.m, plan.ID, func(s Status) bool { return s.Finished })
	if !errors.Is(materializeErr, syscall.EMFILE) || !status.ArtifactRetained || !status.CleanupRetryAllowed {
		t.Fatal("partial allocation failure lost recovery state")
	}
	if f.starts.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("partial allocation failure launched or probed")
	}
	blocked.Store(false)
	status, err := f.m.RetryCleanup(plan.ID)
	if err != nil || status.ArtifactRetained || f.m.ResourcesPending() || !f.m.Wait(context.Background()) {
		t.Fatal("manager did not release recovered artifact", err)
	}
	next := f.plan()
	defer next.Close()
	if !next.Allowed {
		t.Fatal("recovered manager still blocked new plans")
	}
}
