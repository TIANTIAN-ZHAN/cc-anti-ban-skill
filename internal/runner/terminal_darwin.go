//go:build darwin

package runner

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type terminalState struct{ modes [3]syscall.Termios }

func terminalIOCTL(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if err != 0 {
		return err
	}
	return nil
}

func captureTerminal(files [3]*os.File) (terminalState, error) {
	var state terminalState
	var device uint64
	for i, f := range files {
		if f == nil {
			return state, errors.New("missing_terminal")
		}
		info, err := f.Stat()
		if err != nil {
			return state, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return state, errors.New("terminal_metadata")
		}
		if i == 0 {
			device = uint64(stat.Rdev)
		} else if uint64(stat.Rdev) != device {
			return state, errors.New("terminal_mismatch")
		}
		if err := terminalIOCTL(f.Fd(), syscall.TIOCGETA, unsafe.Pointer(&state.modes[i])); err != nil {
			return state, err
		}
	}
	var foreground int32
	if err := terminalIOCTL(files[0].Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&foreground)); err != nil {
		return state, err
	}
	if int(foreground) != syscall.Getpgrp() {
		return state, errors.New("foreground_terminal_required")
	}
	return state, nil
}

func restoreTerminal(files [3]*os.File, state terminalState) error {
	// A CLI can hand foreground ownership to a different group. Never steal it
	// back or write termios as a background process. Catch SIGTTOU for the small
	// check/write race as well; a refused ioctl remains an unconfirmed restore.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTTOU)
	defer signal.Stop(signals)
	for i, f := range files {
		var foreground int32
		if err := terminalIOCTL(f.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&foreground)); err != nil {
			return err
		}
		if int(foreground) != syscall.Getpgrp() {
			return errors.New("terminal_foreground_changed")
		}
		if err := terminalIOCTL(f.Fd(), syscall.TIOCSETA, unsafe.Pointer(&state.modes[i])); err != nil {
			return err
		}
	}
	return nil
}
