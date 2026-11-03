//go:build darwin && cgo

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestArtifactCreationResourceRecovery(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"mkdir", "0", "1", "2", "3"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			home := t.TempDir()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestArtifactCreationResourceChild$")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + home, "CC_ARTIFACT_CREATION_STAGE=" + stage}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated creation recovery: %v %s", err, out)
			}
		})
	}
}

// RLIMIT changes are confined to the disposable child, never the test driver
// or the user's terminal/processes. No CLI, configuration or network is used.
func TestArtifactCreationResourceChild(t *testing.T) {
	stage := os.Getenv("CC_ARTIFACT_CREATION_STAGE")
	if stage == "" {
		t.Skip("isolated resource child")
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if stage == "mkdir" {
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(root, 0700)
		p, writer, err := newPreparedFile()
		if p != nil || writer != nil || !errors.Is(err, syscall.EACCES) {
			t.Fatalf("mkdir failure: %v %v %v", p != nil, writer != nil, err)
		}
		return
	}
	slots, err := strconv.Atoi(stage)
	if err != nil || slots < 0 || slots > 3 {
		t.Fatal("invalid fixture stage")
	}
	var before syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		t.Fatal(err)
	}
	limit := before
	limit.Cur = 64
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	var blockers []*os.File
	restore := func() {
		syscall.Setrlimit(syscall.RLIMIT_NOFILE, &before)
		for i, f := range blockers {
			if f != nil {
				f.Close()
				blockers[i] = nil
			}
		}
	}
	defer restore()
	for {
		f, err := os.Open(os.DevNull)
		if errors.Is(err, syscall.EMFILE) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		blockers = append(blockers, f)
	}
	if len(blockers) < slots {
		t.Fatal("fixture descriptor capacity")
	}
	for i := len(blockers) - slots; i < len(blockers); i++ {
		blockers[i].Close()
		blockers[i] = nil
	}
	p, writer, creationErr := newPreparedFile()
	restore()
	if writer != nil {
		writer.Close()
		t.Fatal("unexpected successful creation")
	}
	if !errors.Is(creationErr, syscall.EMFILE) {
		t.Fatalf("wrong failure: %v", creationErr)
	}
	if slots == 0 {
		if p != nil {
			t.Fatal("parent failure retained an artifact")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("parent failure created files")
		}
		return
	}
	if p == nil {
		t.Fatal("lost partial artifact")
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error("fixture cleanup", err)
		}
	}()
	if slots == 1 {
		if p.artifact.directory != nil || p.artifact.directoryIdentity == nil {
			t.Fatal("directory identity not retained")
		}
		original, saved, foreign := p.temporary, filepath.Join(root, "saved"), filepath.Join(root, "foreign")
		if err := os.Rename(original, saved); err != nil {
			t.Fatal(err)
		}
		sentinelDir(t, original)
		if p.Close() == nil {
			t.Fatal("reopened a replacement directory")
		}
		assertSentinel(t, original)
		if err := os.Rename(original, foreign); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(saved, original); err != nil {
			t.Fatal(err)
		}
		defer assertSentinel(t, foreign)
	} else if slots == 2 {
		if !p.artifact.entryRemoved || p.artifact.entry != nil {
			t.Fatal("uncreated leaf incorrectly owned")
		}
		extra := filepath.Join(p.temporary, "foreign-entry")
		if err := os.WriteFile(extra, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if p.Close() == nil {
			t.Fatal("nonempty partial directory removed")
		}
		extra = filepath.Join(root, p.artifact.base, "foreign-entry")
		if data, err := os.ReadFile(extra); err != nil || string(data) != "keep" {
			t.Fatal("unexpected entry changed")
		}
		if err := os.Remove(extra); err != nil {
			t.Fatal(err)
		}
	} else if p.artifact.entry == nil || p.artifact.entryRemoved {
		t.Fatal("duplicate failure lost creation FD")
	}
	if err := p.Close(); err != nil {
		t.Fatal("resource recovery did not clean", err)
	}
	if !p.removed || p.artifact.parent != nil || p.artifact.directory != nil || p.artifact.entry != nil {
		t.Fatal("cleanup retained handles")
	}
	if err := p.Close(); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}
