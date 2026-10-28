//go:build darwin

package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type receipt struct {
	View               View
	Unchanged          bool
	Restored           bool
	DuplicateRejected  bool
	InterruptOwnership bool
	CompanionMatched   bool
	StressRuns         int
}

func TestMain(m *testing.M) {
	switch os.Getenv("CC_RUNNER_TEST_MODE") {
	case "fake":
		fakeCLI()
		return
	case "supervisor":
		fakeSupervisor()
		return
	case "companion":
		events := os.NewFile(3, "fixture-events")
		signals := make(chan os.Signal, 4)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		fmt.Fprintln(events, "ready")
		for sig := range signals {
			fmt.Fprintln(events, sig.String())
		}
		return
	case "foreground-holder":
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func fakeCLI() {
	if expected := os.Getenv("EXPECTED_CWD_INO"); expected != "" {
		info, e := os.Stat(".")
		if e != nil || strconv.FormatUint(info.Sys().(*syscall.Stat_t).Ino, 10) != expected {
			os.Exit(94)
		}
	}
	if os.Getenv("https_proxy") != "http://127.0.0.1:4567" || os.Getenv("HTTPS_PROXY") != "http://127.0.0.1:4567" || os.Getenv("FAKE_INHERITED_SECRET") != "fictional-only" {
		os.Exit(90)
	}
	mode := os.Getenv("PTY_SCENARIO")
	sig := make(chan os.Signal, 1)
	if mode == "interrupt" || mode == "group-interrupt" {
		signal.Notify(sig, os.Interrupt)
	}
	if mode == "raw-stop" || mode == "group-stop" {
		var term syscall.Termios
		if terminalIOCTL(os.Stdin.Fd(), syscall.TIOCGETA, unsafe.Pointer(&term)) != nil {
			os.Exit(91)
		}
		term.Lflag &^= syscall.ICANON | syscall.ECHO
		if terminalIOCTL(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&term)) != nil {
			os.Exit(92)
		}
		signal.Ignore(syscall.SIGTERM) // Exercise bounded root-only escalation.
	}
	fmt.Println("FAKE_READY")
	switch mode {
	case "instant":
		os.Exit(0)
	case "foreground-lost":
		pid, _ := strconv.Atoi(os.Getenv("HOLDER_PID"))
		group := int32(pid)
		if terminalIOCTL(os.Stdin.Fd(), syscall.TIOCSPGRP, unsafe.Pointer(&group)) != nil {
			os.Exit(93)
		}
		os.Exit(8)
	case "interrupt", "group-interrupt":
		<-sig
		fmt.Println("FAKE_INTERRUPTED")
		os.Exit(42)
	case "raw-stop", "group-stop":
		for {
			time.Sleep(time.Second)
		}
	default:
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Println("FAKE_ECHO:" + strings.TrimSpace(line))
		os.Exit(7)
	}
}

func fakeSupervisor() {
	// This process owns a fresh controlling fixture PTY, not the user's terminal.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	r := New(os.Stdin, os.Stdout, os.Stderr)
	before, err := captureTerminal(r.files)
	if err != nil {
		os.Exit(80)
	}
	path, _ := os.Executable()
	dir := filepath.Join(os.Getenv("HOME"), "project")
	if os.MkdirAll(dir, 0700) != nil {
		os.Exit(83)
	}
	var holder *exec.Cmd
	if os.Getenv("PTY_SCENARIO") == "foreground-lost" {
		holder = exec.Command(path)
		holder.Env = []string{"CC_RUNNER_TEST_MODE=foreground-holder"}
		holder.Stdin, holder.Stdout, holder.Stderr = os.Stdin, os.Stdout, os.Stderr
		holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if holder.Start() != nil {
			os.Exit(82)
		}
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "CC_RUNNER_TEST_MODE=fake", "PTY_SCENARIO=" + os.Getenv("PTY_SCENARIO"), "https_proxy=http://127.0.0.1:4567", "HTTPS_PROXY=http://127.0.0.1:4567", "FAKE_INHERITED_SECRET=fictional-only"}
	if holder != nil {
		env = append(env, "HOLDER_PID="+strconv.Itoa(holder.Process.Pid))
	}
	original := os.Getenv("https_proxy")
	var p *Process
	mode := os.Getenv("PTY_SCENARIO")
	if mode == "short-stress" {
		fmt.Println("FAKE_READY")
		stressEnv := append([]string(nil), env...)
		for i, v := range stressEnv {
			if strings.HasPrefix(v, "PTY_SCENARIO=") {
				stressEnv[i] = "PTY_SCENARIO=instant"
			}
		}
		for i := 0; i < 12; i++ {
			p, err = startFixture(r, path, dir, stressEnv)
			if err != nil {
				os.Exit(81)
			}
			go p.Stop()
			select {
			case <-p.Done():
			case <-time.After(3 * time.Second):
				os.Exit(96)
			}
		}
		after, e := captureTerminal(r.files)
		result := os.NewFile(4, "fixture-result")
		_ = json.NewEncoder(result).Encode(receipt{View: p.View(), Unchanged: os.Getenv("https_proxy") == original, Restored: e == nil && before == after, InterruptOwnership: r.OwnsInterrupts(), StressRuns: 12})
		result.Close()
		os.Exit(0)
	}
	var companion *exec.Cmd
	var companionEvents chan string
	var eventRead, eventWrite *os.File
	if mode == "group-interrupt" || mode == "group-stop" {
		eventRead, eventWrite, err = os.Pipe()
		if err != nil {
			os.Exit(97)
		}
		companion = exec.Command(path)
		companion.Env = []string{"CC_RUNNER_TEST_MODE=companion"}
		companion.Stdin, companion.Stdout, companion.Stderr = os.Stdin, os.Stdout, os.Stderr
		companion.ExtraFiles = []*os.File{eventWrite}
		if companion.Start() != nil {
			os.Exit(97)
		}
		eventWrite.Close()
		companionEvents = make(chan string, 4)
		go func() {
			scanner := bufio.NewScanner(eventRead)
			for scanner.Scan() {
				companionEvents <- scanner.Text()
			}
		}()
		select {
		case <-companionEvents:
		case <-time.After(2 * time.Second):
			os.Exit(97)
		}
	}

	if mode == "path-swapped" || mode == "directory-swapped" {
		original := filepath.Join(os.Getenv("HOME"), "approved-entry")
		input, e := os.Open(path)
		if e != nil {
			os.Exit(84)
		}
		output, e := os.OpenFile(original, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if e != nil {
			os.Exit(85)
		}
		_, e = io.Copy(output, input)
		input.Close()
		output.Close()
		if e != nil {
			os.Exit(86)
		}
		info, e := os.Stat(dir)
		if e != nil {
			os.Exit(87)
		}
		env = append(env, "EXPECTED_CWD_INO="+strconv.FormatUint(info.Sys().(*syscall.Stat_t).Ino, 10))
		image, e := OpenImage(context.Background(), original, dir)
		if e != nil || !image.Consume() {
			os.Exit(88)
		}
		if mode == "path-swapped" {
			if os.Rename(original, original+"-approved") != nil {
				os.Exit(89)
			}
			if os.WriteFile(original, []byte("#!/bin/sh\nprintf UNAPPROVED_B\nexit 99\n"), 0700) != nil {
				os.Exit(89)
			}
		} else {
			if os.Rename(dir, dir+"-approved") != nil || os.Mkdir(dir, 0700) != nil {
				os.Exit(89)
			}
		}
		prepared, e := image.Materialize(context.Background())
		image.Close()
		if e != nil {
			os.Exit(89)
		}
		p, err = r.Start(prepared, env)
		if err != nil {
			prepared.Close()
		}
	} else {
		p, err = startFixture(r, path, dir, env)
	}
	if err != nil {
		os.Exit(81)
	}
	_, duplicate := startFixture(r, path, dir, env)
	control := os.NewFile(3, "fixture-control")
	result := os.NewFile(4, "fixture-result")
	go func() {
		var b [1]byte
		if _, e := control.Read(b[:]); e == nil && b[0] == 's' {
			p.Stop()
		}
	}()
	<-p.Done()
	after, e := captureTerminal(r.files)
	if holder != nil {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	}
	companionMatched := true
	if companion != nil {
		if mode == "group-interrupt" {
			select {
			case event := <-companionEvents:
				companionMatched = event == "interrupt"
			case <-time.After(time.Second):
				companionMatched = false
			}
		} else {
			select {
			case <-companionEvents:
				companionMatched = false
			case <-time.After(100 * time.Millisecond):
			}
		}
		if companion.Process.Signal(syscall.Signal(0)) != nil {
			companionMatched = false
		}
		_ = companion.Process.Kill()
		_ = companion.Wait()
		eventRead.Close()
	}
	_ = json.NewEncoder(result).Encode(receipt{View: p.View(), Unchanged: os.Getenv("https_proxy") == original, Restored: e == nil && before == after, DuplicateRejected: duplicate != nil, InterruptOwnership: r.OwnsInterrupts(), CompanionMatched: companionMatched})
	_ = result.Close()
	_ = control.Close()
	os.Exit(0)
}

func fixturePTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := terminalIOCTL(master.Fd(), syscall.TIOCPTYGRANT, nil); err != nil {
		t.Fatal(err)
	}
	if err := terminalIOCTL(master.Fd(), syscall.TIOCPTYUNLK, nil); err != nil {
		t.Fatal(err)
	}
	var name [128]byte
	if err := terminalIOCTL(master.Fd(), syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimRight(string(name[:]), "\x00")
	slave, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func TestRealPTYFakeCLI(t *testing.T) {
	for _, mode := range []string{"natural", "interrupt", "raw-stop", "foreground-lost", "path-swapped", "directory-swapped", "short-stress", "group-interrupt", "group-stop"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := fixturePTY(t)
			controlR, controlW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			resultR, resultW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer controlR.Close()
			defer controlW.Close()
			defer resultR.Close()
			defer resultW.Close()
			path, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			marker := filepath.Join(home, "unchanged")
			if err := os.WriteFile(marker, []byte("fixture-original"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(path)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + os.Getenv("TMPDIR"), "CC_RUNNER_TEST_MODE=supervisor", "PTY_SCENARIO=" + mode, "https_proxy=http://unchanged.invalid"}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			cmd.ExtraFiles = []*os.File{controlR, resultW}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = controlR.Close()
			_ = resultW.Close()
			finished := make(chan error, 1)
			go func() { finished <- cmd.Wait() }()
			defer func() {
				select {
				case <-finished:
				default:
					_ = master.Close()
					_ = slave.Close()
					select {
					case <-finished:
					case <-time.After(5 * time.Second):
						t.Error("fixture did not exit after its owned PTY closed")
					}
				}
			}()
			ready := make(chan bool, 1)
			echoed := make(chan bool, 1)
			go func() {
				reader := bufio.NewReader(master)
				sentReady := false
				for {
					line, e := reader.ReadString('\n')
					if strings.Contains(line, "FAKE_READY") && !sentReady {
						ready <- true
						sentReady = true
					}
					if strings.Contains(line, "FAKE_ECHO:hello fixture") {
						select {
						case echoed <- true:
						default:
						}
					}
					if e != nil {
						if !sentReady {
							ready <- false
						}
						return
					}
				}
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("fake CLI did not become ready")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("PTY startup timeout")
			}
			switch mode {
			case "short-stress":
			case "interrupt", "group-interrupt":
				_, err = master.Write([]byte{3})
			case "raw-stop", "group-stop":
				_, err = controlW.Write([]byte{'s'})
			default:
				_, err = master.Write([]byte("hello fixture\n"))
			}
			if err != nil {
				t.Fatal(err)
			}
			got := make(chan receipt, 1)
			go func() { var r receipt; _ = json.NewDecoder(resultR).Decode(&r); got <- r }()
			var r receipt
			select {
			case r = <-got:
			case <-time.After(18 * time.Second):
				t.Fatal("PTY finish timeout")
			}
			wantRestored := mode != "foreground-lost"
			if !r.View.EnvironmentDelivered || r.View.TerminalRestored != wantRestored || !r.Unchanged || r.Restored != wantRestored || (!r.DuplicateRejected && mode != "short-stress") || !r.InterruptOwnership {
				t.Fatalf("bad receipt: %+v", r)
			}
			want := 7
			if mode == "interrupt" || mode == "group-interrupt" {
				want = 42
			}
			if mode == "foreground-lost" {
				want = 8
			}
			if mode == "raw-stop" || mode == "group-stop" {
				want = -1
				if r.View.State != "stopped" {
					t.Fatal("root not stopped")
				}
			}
			if (mode == "group-interrupt" || mode == "group-stop") && !r.CompanionMatched {
				t.Fatal("terminal group broadcast confused with program root signal")
			}
			if mode == "short-stress" {
				if r.StressRuns != 12 || r.View.ExitCode == nil || (*r.View.ExitCode != 0 && *r.View.ExitCode != -1) {
					t.Fatal("short child stress failed")
				}
				want = *r.View.ExitCode
			}
			if r.View.ExitCode == nil || *r.View.ExitCode != want {
				t.Fatalf("exit status: %+v", r.View)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal("supervisor failed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor exit timeout")
			}
			finished <- nil
			if mode == "natural" {
				select {
				case <-echoed:
				case <-time.After(time.Second):
					t.Fatal("terminal output missing")
				}
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "fixture-original" {
				t.Fatal("fixture file changed")
			}
		})
	}
}

func TestNonTTYRejectsStartup(t *testing.T) {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := New(f, f, f)
	if r.Available() {
		t.Fatal("non-TTY accepted")
	}
	if _, err := r.Start(nil, nil); err == nil {
		t.Fatal("non-TTY started")
	}
}

func startFixture(r *Runner, path, dir string, env []string) (*Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	image, e := OpenImage(ctx, path, dir)
	if e != nil {
		return nil, e
	}
	defer image.Close()
	if !image.Consume() {
		return nil, fmt.Errorf("fixture consume failed")
	}
	prepared, e := image.Materialize(ctx)
	if e != nil {
		return nil, e
	}
	p, e := r.Start(prepared, env)
	if e != nil {
		prepared.Close()
	}
	return p, e
}
