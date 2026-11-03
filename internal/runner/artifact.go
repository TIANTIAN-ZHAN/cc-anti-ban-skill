package runner

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// These descriptors, not the mutable temporary pathname, own the cleanup.
// Names are captured atomically into random private slots and checked after the
// move. Never recursively delete. This is not an isolation boundary against an
// arbitrary same-UID adversary entering the private slots themselves.
type artifact struct {
	parent, directory, entry        *os.File
	directoryIdentity               *artifactIdentity
	base, leaf                      string
	captured, entryRemoved, removed bool
	undo                            *captureUndo
	beforeCapture                   func(string) // Package-private deterministic race injection.
	afterCapture                    func(string)
}
type artifactIdentity struct {
	device, inode       uint64
	generation          uint32
	birthSec, birthNsec int64
}

type captureUndo struct {
	directory *os.File
	from, to  string
}

func privateName(prefix string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func newPreparedFile() (*Prepared, *os.File, error) {
	parentPath, e := filepath.EvalSymlinks(os.TempDir())
	if e != nil {
		return nil, nil, e
	}
	parent, e := os.OpenFile(parentPath, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, nil, e
	}
	base, e := privateName("cc-health-approved-")
	if e != nil {
		parent.Close()
		return nil, nil, e
	}
	if e = artifactMkdir(parent, base); e != nil {
		parent.Close()
		return nil, nil, e
	}
	// No leaf is owned until O_EXCL creation succeeds. An empty directory from a
	// failed creation can be removed, but an unexpected entry must be retained.
	a := &artifact{parent: parent, base: base, leaf: "entry", entryRemoved: true}
	p := &Prepared{temporary: filepath.Join(parentPath, base), artifact: a}
	p.path = filepath.Join(p.temporary, "entry")
	// fstatat does not allocate an FD: retain identity even if open hits EMFILE.
	// Creation in a random 0700 slot is not a same-UID isolation boundary.
	a.directoryIdentity, e = artifactDirectoryIdentity(parent, base)
	if e != nil {
		return p, nil, e
	}
	if e = a.openDirectory(); e != nil {
		return p, nil, e
	}
	target, e := artifactOpen(a.directory, "entry", false, true)
	if e != nil {
		return p, nil, e
	}
	// Keep the original creation FD for identity. The writer receives a duplicate
	// of that object, never a second pathname lookup. Dup failure cannot discard it.
	a.entry = target
	a.entryRemoved = false
	writer, e := artifactDuplicate(target)
	if e != nil {
		return p, nil, e
	}
	return p, writer, nil
}

func (a *artifact) openDirectory() error {
	if a.directory != nil {
		return nil
	}
	if a.parent == nil || a.directoryIdentity == nil {
		return errors.New("cleanup_identity_unconfirmed")
	}
	directory, err := artifactOpen(a.parent, a.base, true, false)
	if err != nil {
		return err
	}
	if !a.directoryIdentity.matches(directory) {
		directory.Close()
		return errors.New("cleanup_identity_changed")
	}
	a.directory = directory
	return nil
}
func sameAt(parent *os.File, name string, expected *os.File, directory bool) bool {
	if parent == nil || expected == nil {
		return false
	}
	found, e := artifactOpen(parent, name, directory, false)
	if e != nil {
		return false
	}
	defer found.Close()
	a, e1 := found.Stat()
	b, e2 := expected.Stat()
	return e1 == nil && e2 == nil && os.SameFile(a, b)
}

// If the source changed between inspection and rename, restore it without
// overwriting anything. A failed restore is retained, never treated as our file.
func (a *artifact) capture(parent *os.File, from string, expected *os.File, directory bool, stage string) (string, error) {
	if !sameAt(parent, from, expected, directory) {
		return "", errors.New("cleanup_identity_changed")
	}
	to, e := privateName(".cc-health-delete-")
	if e != nil {
		return "", errors.New("private_copy_cleanup_failed")
	}
	if a.beforeCapture != nil {
		a.beforeCapture(stage)
	}
	if e = artifactRename(parent, from, to); e != nil {
		return "", errors.New("private_copy_cleanup_failed")
	}
	if a.afterCapture != nil {
		a.afterCapture(stage)
	}
	if sameAt(parent, to, expected, directory) {
		return to, nil
	}
	if e = artifactRename(parent, to, from); e != nil {
		a.undo = &captureUndo{parent, to, from}
		return "", errors.New("cleanup_restore_pending")
	}
	return "", errors.New("cleanup_identity_changed")
}
func (a *artifact) cleanup() error {
	if a.removed {
		return nil
	}
	if a.undo != nil {
		if artifactRename(a.undo.directory, a.undo.from, a.undo.to) != nil {
			return errors.New("cleanup_restore_pending")
		}
		a.undo = nil
		return errors.New("cleanup_identity_changed") // Require a fresh retry after restoration.
	}
	if a.parent == nil {
		return errors.New("cleanup_identity_unconfirmed")
	}
	if err := a.openDirectory(); err != nil {
		return errors.New("cleanup_identity_unconfirmed")
	}
	if !a.captured {
		name, e := a.capture(a.parent, a.base, a.directory, true, "directory")
		if e != nil {
			return e
		}
		a.base = name
		a.captured = true
	}
	// A fresh directory stream is bound to the held object, never its old pathname.
	stream, e := artifactOpen(a.directory, ".", true, false)
	if e != nil {
		return errors.New("cleanup_identity_unconfirmed")
	}
	names, readErr := stream.Readdirnames(2)
	stream.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.New("cleanup_contents_changed")
	}
	if a.entryRemoved {
		if len(names) != 0 {
			return errors.New("cleanup_contents_changed")
		}
	} else {
		if len(names) != 1 || names[0] != a.leaf {
			return errors.New("cleanup_contents_changed")
		}
		name, e := a.capture(a.directory, a.leaf, a.entry, false, "entry")
		if e != nil {
			return e
		}
		a.leaf = name
		// Only the captured, verified leaf; never follow symlinks or recurse.
		if e = artifactUnlink(a.directory, a.leaf, false); e != nil {
			return errors.New("private_copy_cleanup_failed")
		}
		a.entryRemoved = true
		a.entry.Close()
		a.entry = nil
	}
	if !sameAt(a.parent, a.base, a.directory, true) {
		return errors.New("cleanup_identity_changed")
	}
	// AT_REMOVEDIR refuses any nonempty replacement. No traversal is possible.
	if artifactUnlink(a.parent, a.base, true) != nil {
		return errors.New("private_copy_cleanup_failed")
	}
	a.removed = true
	a.directory.Close()
	a.parent.Close()
	a.directory = nil
	a.parent = nil
	return nil
}
