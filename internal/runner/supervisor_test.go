package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestReapedPIDIsNeverSignalledAfterReuse(t *testing.T) {
	var signals []syscall.Signal
	p := &Process{pid: 123, done: make(chan struct{}), view: View{State: "running", ProgramStopAllowed: true}, signalPolicy: func() bool { return true }, poll: func(int) (bool, int, error) { return true, 0, nil }, signal: func(_ int, s syscall.Signal) error { signals = append(signals, s); return nil }}
	p.Stop() // The poll reaps the old child; the fake OS can now reuse PID 123.
	p.mu.Lock()
	p.signalLocked(syscall.SIGKILL)
	p.mu.Unlock()
	p.Stop()
	if len(signals) != 0 || !p.exitConfirmed || p.View().State != "exited" {
		t.Fatal("signal escaped terminal ownership state")
	}
}
func TestWaitAndSignalCannotInterleaveAcrossReap(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	exited := false
	calls := 0
	p := &Process{pid: 123, done: make(chan struct{}), view: View{State: "running", ProgramStopAllowed: true}, signalPolicy: func() bool { return true }, poll: func(int) (bool, int, error) { mu.Lock(); defer mu.Unlock(); return exited, 0, nil }, signal: func(int, syscall.Signal) error {
		close(entered)
		<-release
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}}
	finished := make(chan struct{})
	go func() { p.Stop(); close(finished) }()
	<-entered
	mu.Lock()
	exited = true
	mu.Unlock()
	reaped := make(chan struct{})
	go func() { p.mu.Lock(); p.pollLocked(); p.mu.Unlock(); close(reaped) }()
	close(release)
	<-finished
	<-reaped
	close(p.done)
	p.Stop()
	if calls != 1 || !p.exitConfirmed {
		t.Fatal("reap/signal lock contract failed")
	}
}
func TestUnknownReaperPolicyOrOwnershipDisablesSignal(t *testing.T) {
	for _, waitError := range []bool{false, true} {
		calls := 0
		p := &Process{pid: 123, done: make(chan struct{}), view: View{State: "running", ProgramStopAllowed: true}, signalPolicy: func() bool { return false }, poll: func(int) (bool, int, error) {
			if waitError {
				return false, 0, syscall.ECHILD
			}
			return false, 0, nil
		}, signal: func(int, syscall.Signal) error { calls++; return nil }}
		p.Stop()
		if calls != 0 || p.View().ProgramStopAllowed {
			t.Fatal("unsafe process signal permitted")
		}
	}
}
func TestApprovedSourceMutationRejected(t *testing.T) {
	if !nativeAvailable() {
		t.Skip("native image adapter")
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	source, e := os.Open(binary)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	root := t.TempDir()
	path := filepath.Join(root, "native-entry")
	target, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(target, source)
	target.Close()
	if e != nil {
		t.Fatal(e)
	}
	image, e := OpenImage(context.Background(), path, root)
	if e != nil {
		t.Fatal(e)
	}
	defer image.Close()
	if !image.Consume() {
		t.Fatal("consume")
	}
	if e = os.WriteFile(path, []byte("changed in the same inode"), 0700); e != nil {
		t.Fatal(e)
	}
	if prepared, e := image.Materialize(context.Background()); e == nil {
		prepared.Close()
		t.Fatal("mutated approved source accepted")
	}
}
func TestCleanupFailureIsRetainedAndRetryable(t *testing.T) {
	prepared, target, e := newPreparedFile()
	if e != nil {
		t.Fatal(e)
	}
	target.Close()
	t.Cleanup(func() { prepared.remove = nil; _ = prepared.Close() })
	attempts := 0
	prepared.remove = func(string) error {
		attempts++
		if attempts == 1 {
			return errors.New("fixture deletion denied")
		}
		return nil
	}
	p := &Process{exitConfirmed: true, reaped: true, prepared: prepared, view: View{State: "exited", ArtifactRetained: true}}
	if p.RetryCleanup() == nil || !p.View().ArtifactRetained {
		t.Fatal("cleanup failure hidden")
	}
	if p.RetryCleanup() != nil || p.View().ArtifactRetained || attempts != 2 {
		t.Fatal("cleanup could not recover")
	}
}

func TestUnsupportedScriptNeverBecomesApprovedNativeEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shim")
	if e := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if image, e := OpenImage(context.Background(), path, filepath.Dir(path)); e == nil {
		image.Close()
		t.Fatal("script was accepted as standalone native executable")
	}
}

func TestDirectoryCloseFailureIsNotReportedClean(t *testing.T) {
	path := t.TempDir()
	directory, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	p := &Prepared{temporary: path, directory: directory, closeDirectory: func(f *os.File) error { _ = f.Close(); return errors.New("fixture close uncertainty") }, remove: func(string) error { return nil }}
	if p.Close() == nil || p.Close() == nil {
		t.Fatal("uncertain descriptor cleanup promoted to success")
	}
}
