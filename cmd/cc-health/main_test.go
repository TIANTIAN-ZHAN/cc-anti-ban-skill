package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestLateInteractiveInterruptKeepsService(t *testing.T) {
	signals := make(chan os.Signal, 2)
	signals <- os.Interrupt
	signals <- syscall.SIGTERM
	// The root has already exited, but interactive ownership is latched. A
	// queued Ctrl-C must be ignored regardless of when the loop consumes it.
	if got := serviceStopSignal(context.Background(), signals, func() bool { return true }); got != syscall.SIGTERM {
		t.Fatal("late child interrupt closed service")
	}
	signals <- os.Interrupt
	if got := serviceStopSignal(context.Background(), signals, func() bool { return false }); got != os.Interrupt {
		t.Fatal("initial read-only service cannot be interrupted")
	}
}

func TestCLIRejectsUnconfirmedOrNetworkFlags(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"check", "--network"}, {"serve", "--host=0.0.0.0"}, {"probe", "https://arbitrary.invalid"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("unexpected exit %d", code)
		}
		if strings.Contains(out.String(), "private") {
			t.Fatal("unexpected data")
		}
	}
	var out, err bytes.Buffer
	if run([]string{"--help"}, &out, &err) != 0 || !strings.Contains(out.String(), "--local-consent") {
		t.Fatal("help missing consent boundary")
	}
}
