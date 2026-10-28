package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const imageLimit = int64(512 * 1024 * 1024)

// Image holds the approved source and directory objects across confirmation.
// It does not claim protection against a malicious process with the same UID.
type Image struct {
	mu                      sync.Mutex
	source, directory       *os.File
	Path, Directory, Digest string
	closed, consumed        bool
	size                    int64
	timer                   *time.Timer
	remove                  func(string) error
}

func OpenImage(ctx context.Context, path, dir string) (*Image, error) {
	if !nativeAvailable() {
		return nil, errors.New("native_adapter_unavailable")
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, e
	}
	resolved, e = filepath.Abs(resolved)
	if e != nil {
		return nil, e
	}
	directory, e := filepath.EvalSymlinks(dir)
	if e != nil {
		return nil, e
	}
	directory, e = filepath.Abs(directory)
	if e != nil {
		return nil, e
	}
	source, e := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	dirFile, e := os.OpenFile(directory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		source.Close()
		return nil, e
	}
	image := &Image{source: source, directory: dirFile, Path: resolved, Directory: directory}
	info, e := source.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() > imageLimit {
		image.Close()
		return nil, errors.New("unsupported_native_entry")
	}
	image.size = info.Size()
	if e = checkNative(source); e != nil {
		image.Close()
		return nil, e
	}
	image.Digest, e = hashImage(ctx, source)
	if e != nil {
		image.Close()
		return nil, e
	}
	return image, nil
}
func hashImage(ctx context.Context, file *os.File) (string, error) {
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > imageLimit {
		return "", errors.New("image_unavailable")
	}
	h := sha256.New()
	reader := io.NewSectionReader(file, 0, info.Size())
	buffer := make([]byte, 65536)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, e := reader.Read(buffer)
		if n > 0 {
			_, _ = h.Write(buffer[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	end, e := file.Stat()
	if e != nil || end.Size() != info.Size() || !end.ModTime().Equal(info.ModTime()) {
		return "", errors.New("image_changed_during_read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (i *Image) ExpireAfter(duration time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	i.timer = time.AfterFunc(duration, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if !i.consumed {
			i.closeLocked()
		}
	})
}
func (i *Image) Consume() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.consumed {
		return false
	}
	i.consumed = true
	if i.timer != nil {
		i.timer.Stop()
	}
	return true
}
func (i *Image) Close() { i.mu.Lock(); defer i.mu.Unlock(); i.closeLocked() }
func (i *Image) closeLocked() {
	if i.closed {
		return
	}
	i.closed = true
	if i.timer != nil {
		i.timer.Stop()
	}
	_ = i.source.Close()
	_ = i.directory.Close()
}

type Prepared struct {
	artifact        *artifact
	path, temporary string
	directory       *os.File
	mu              sync.Mutex
	remove          func(string) error
	cleanupErr      error
	removed         bool
	directoryErr    error
	closeDirectory  func(*os.File) error
}

func (p *Prepared) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.directory != nil {
		closeDirectory := p.closeDirectory
		if closeDirectory == nil {
			closeDirectory = func(file *os.File) error { return file.Close() }
		}
		if e := closeDirectory(p.directory); e != nil && !errors.Is(e, os.ErrClosed) {
			p.directoryErr = errors.New("directory_close_unconfirmed")
		}
		p.directory = nil
	}
	if !p.removed {
		if p.remove != nil {
			if e := p.remove(p.temporary); e != nil {
				p.cleanupErr = errors.New("private_copy_cleanup_failed")
				return p.cleanupErr
			}
		}
		if p.artifact == nil {
			p.cleanupErr = errors.New("cleanup_identity_unconfirmed")
			return p.cleanupErr
		}
		if e := p.artifact.cleanup(); e != nil {
			p.cleanupErr = e
			return e
		}
		p.removed = true
	}
	if p.directoryErr != nil {
		p.cleanupErr = p.directoryErr
		return p.cleanupErr
	}
	p.cleanupErr = nil
	return nil
}

func (p *Prepared) CleanupCode() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleanupErr == nil {
		return ""
	}
	return p.cleanupErr.Error()
}

// Materialize never reopens the original executable or directory pathname.
func (i *Image) Materialize(ctx context.Context) (*Prepared, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || !i.consumed || ctx.Err() != nil {
		return nil, errors.New("approved_image_unavailable")
	}
	prepared, target, e := newPreparedFile()
	if prepared != nil {
		prepared.remove = i.remove
	}
	if e != nil {
		if prepared != nil && prepared.Close() != nil {
			return prepared, e
		}
		return nil, e
	}
	fail := func(err error) (*Prepared, error) {
		if target.Close() != nil {
			prepared.directoryErr = errors.New("copy_close_unconfirmed")
		}
		if prepared.Close() != nil {
			return prepared, err
		}
		return nil, err
	}
	reader := io.NewSectionReader(i.source, 0, i.size)
	buffer := make([]byte, 65536)
	for {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, e = target.Write(buffer[:n]); e != nil {
				return fail(e)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
	}
	if e = copyApproved(i.source, target); e != nil {
		return fail(errors.New("approved_copy_failed"))
	}
	got, e := hashImage(ctx, target)
	if e != nil || got != i.Digest {
		return fail(errors.New("approved_source_changed"))
	}
	if e = checkNative(target); e != nil {
		return fail(e)
	}
	if e = target.Chmod(0500); e != nil {
		return fail(e)
	}
	if e = target.Close(); e != nil {
		prepared.directoryErr = errors.New("copy_close_unconfirmed")
		if prepared.Close() != nil {
			return prepared, e
		}
		return nil, e
	}
	fd, e := syscall.Dup(int(i.directory.Fd()))
	if e != nil {
		if prepared.Close() != nil {
			return prepared, e
		}
		return nil, e
	}
	syscall.CloseOnExec(fd)
	prepared.directory = os.NewFile(uintptr(fd), "approved-directory")
	return prepared, nil
}
