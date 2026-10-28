//go:build !darwin

package runner

import (
	"errors"
	"os"
)

type terminalState struct{}

func captureTerminal([3]*os.File) (terminalState, error) {
	return terminalState{}, errors.New("platform_unsupported")
}
func restoreTerminal([3]*os.File, terminalState) error { return errors.New("platform_unsupported") }
