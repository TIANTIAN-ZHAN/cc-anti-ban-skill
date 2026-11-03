//go:build !darwin || !cgo

package runner

import (
	"errors"
	"os"
)

func nativeAvailable() bool                 { return false }
func reapingPolicySafe() bool               { return false }
func copyApproved(*os.File, *os.File) error { return errors.New("native_adapter_unavailable") }
func spawnApproved(*Prepared, [3]*os.File, []string) (int, error) {
	return 0, errors.New("native_adapter_unavailable")
}

func artifactMkdir(*os.File, string) error { return errors.New("native_adapter_unavailable") }
func artifactDirectoryIdentity(*os.File, string) (*artifactIdentity, error) {
	return nil, errors.New("native_adapter_unavailable")
}
func (*artifactIdentity) matches(*os.File) bool { return false }
func artifactDuplicate(*os.File) (*os.File, error) {
	return nil, errors.New("native_adapter_unavailable")
}
func artifactOpen(*os.File, string, bool, bool) (*os.File, error) {
	return nil, errors.New("native_adapter_unavailable")
}
func artifactRename(*os.File, string, string) error { return errors.New("native_adapter_unavailable") }
func artifactUnlink(*os.File, string, bool) error   { return errors.New("native_adapter_unavailable") }
