//go:build darwin && cgo

package runner

/*
#include <spawn.h>
#include <stdio.h>
#include <sys/stat.h>
#include <signal.h>
#include <copyfile.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdlib.h>
#include <errno.h>
#include <stdint.h>

static int artifact_mkdir(int parent, const char *name) { return mkdirat(parent,name,0700)==0 ? 0 : errno; }
static int artifact_directory_identity(int parent, const char *name, uint64_t *device, uint64_t *inode,
                                      uint32_t *generation, int64_t *birth_sec, int64_t *birth_nsec) {
 struct stat status;
 if (fstatat(parent,name,&status,AT_SYMLINK_NOFOLLOW)!=0) return errno;
 if (!S_ISDIR(status.st_mode)) return ENOTDIR;
 *device=(uint64_t)status.st_dev; *inode=(uint64_t)status.st_ino;
 *generation=status.st_gen;
 *birth_sec=status.st_birthtimespec.tv_sec; *birth_nsec=status.st_birthtimespec.tv_nsec;
 return 0;
}
static int artifact_duplicate(int fd) { return fcntl(fd,F_DUPFD_CLOEXEC,0); }
static int artifact_open(int parent,const char *name,int directory,int create) {
 int flags=O_CLOEXEC|O_NOFOLLOW|O_NONBLOCK;
 flags|=directory ? O_RDONLY|O_DIRECTORY : (create ? O_RDWR|O_CREAT|O_EXCL : O_RDONLY);
 return openat(parent,name,flags,0600);
}
static int artifact_rename(int parent,const char *from,const char *to) {
 return renameatx_np(parent,from,parent,to,RENAME_EXCL)==0 ? 0 : errno;
}
static int artifact_unlink(int parent,const char *name,int directory) {
 return unlinkat(parent,name,directory ? AT_REMOVEDIR : 0)==0 ? 0 : errno;
}
static int reaping_policy_safe(void) {
    struct sigaction action;
    if (sigaction(SIGCHLD, NULL, &action) != 0) return 0;
    return action.sa_handler != SIG_IGN && !(action.sa_flags & SA_NOCLDWAIT);
}
static int copy_approved_file(int source, int target) {
    if (fcopyfile(source, target, NULL, COPYFILE_METADATA) != 0) return errno;
    return 0;
}
static int spawn_approved(pid_t *pid, const char *path, int directory,
                          int input, int output, int errors, char *const env[]) {
    if (!reaping_policy_safe()) return EPERM;
    posix_spawn_file_actions_t files;
    posix_spawnattr_t attrs;
    int rc = posix_spawn_file_actions_init(&files);
    if (rc) return rc;
    rc = posix_spawnattr_init(&attrs);
    if (rc) { posix_spawn_file_actions_destroy(&files); return rc; }
    int original[3] = {input, output, errors};
    int copies[3] = {-1, -1, -1};
    for (int i = 0; i < 3; i++) {
        copies[i] = fcntl(original[i], F_DUPFD_CLOEXEC, 10);
        if (copies[i] < 0) { rc = errno; goto finished; }
        rc = posix_spawn_file_actions_adddup2(&files, copies[i], i);
        if (rc) goto finished;
    }
    // Public macOS 10.15+ API. This binds a directory object, not its pathname.
#if __MAC_OS_X_VERSION_MIN_REQUIRED >= 260000
    rc = posix_spawn_file_actions_addfchdir(&files, directory);
#else
    rc = posix_spawn_file_actions_addfchdir_np(&files, directory);
#endif
    if (rc) goto finished;
    sigset_t empty, defaults;
    sigemptyset(&empty); sigemptyset(&defaults);
    int reset[] = {SIGINT, SIGQUIT, SIGTERM, SIGPIPE, SIGTSTP, SIGTTIN, SIGTTOU, SIGCHLD};
    for (unsigned i = 0; i < sizeof(reset)/sizeof(reset[0]); i++) sigaddset(&defaults, reset[i]);
    rc = posix_spawnattr_setsigmask(&attrs, &empty);
    if (rc) goto finished;
    rc = posix_spawnattr_setsigdefault(&attrs, &defaults);
    if (rc) goto finished;
    rc = posix_spawnattr_setflags(&attrs, POSIX_SPAWN_CLOEXEC_DEFAULT | POSIX_SPAWN_SETSIGMASK | POSIX_SPAWN_SETSIGDEF);
    if (rc) goto finished;
    char *argv[] = {(char *)path, NULL};
    rc = posix_spawn(pid, path, &files, &attrs, argv, env);
finished:
    for (int i = 0; i < 3; i++) if (copies[i] >= 0) close(copies[i]);
    posix_spawnattr_destroy(&attrs);
    posix_spawn_file_actions_destroy(&files);
    return rc;
}
*/
import "C"

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func nativeAvailable() bool   { return true }
func reapingPolicySafe() bool { return C.reaping_policy_safe() != 0 }
func copyApproved(source, target *os.File) error {
	if e := C.copy_approved_file(C.int(source.Fd()), C.int(target.Fd())); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}
func spawnApproved(prepared *Prepared, files [3]*os.File, env []string) (int, error) {
	path := C.CString(prepared.path)
	defer C.free(unsafe.Pointer(path))
	storage := C.calloc(C.size_t(len(env)+1), C.size_t(unsafe.Sizeof(uintptr(0))))
	if storage == nil {
		return 0, syscall.ENOMEM
	}
	defer C.free(storage)
	values := unsafe.Slice((**C.char)(storage), len(env)+1)
	for i, value := range env {
		values[i] = C.CString(value)
		defer C.free(unsafe.Pointer(values[i]))
	}
	var pid C.pid_t
	rc := C.spawn_approved(&pid, path, C.int(prepared.directory.Fd()), C.int(files[0].Fd()), C.int(files[1].Fd()), C.int(files[2].Fd()), (**C.char)(storage))
	runtime.KeepAlive(prepared)
	runtime.KeepAlive(files)
	if rc != 0 {
		return 0, syscall.Errno(rc)
	}
	return int(pid), nil
}

func artifactMkdir(parent *os.File, name string) error {
	text := C.CString(name)
	defer C.free(unsafe.Pointer(text))
	rc := C.artifact_mkdir(C.int(parent.Fd()), text)
	runtime.KeepAlive(parent)
	if rc != 0 {
		return syscall.Errno(rc)
	}
	return nil
}
func artifactDirectoryIdentity(parent *os.File, name string) (*artifactIdentity, error) {
	text := C.CString(name)
	defer C.free(unsafe.Pointer(text))
	var device, inode C.uint64_t
	var generation C.uint32_t
	var birthSec, birthNsec C.int64_t
	rc := C.artifact_directory_identity(C.int(parent.Fd()), text, &device, &inode, &generation, &birthSec, &birthNsec)
	runtime.KeepAlive(parent)
	if rc != 0 {
		return nil, syscall.Errno(rc)
	}
	return &artifactIdentity{device: uint64(device), inode: uint64(inode), generation: uint32(generation), birthSec: int64(birthSec), birthNsec: int64(birthNsec)}, nil
}
func (id *artifactIdentity) matches(file *os.File) bool {
	if id == nil || file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	// A failed initial open has not pinned the inode. Also bind creation metadata
	// so a recycled inode does not acquire the original directory's authority.
	return ok && uint64(stat.Dev) == id.device && uint64(stat.Ino) == id.inode &&
		stat.Gen == id.generation && stat.Birthtimespec.Sec == id.birthSec && stat.Birthtimespec.Nsec == id.birthNsec
}
func artifactDuplicate(file *os.File) (*os.File, error) {
	fd, err := C.artifact_duplicate(C.int(file.Fd()))
	runtime.KeepAlive(file)
	if fd < 0 {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "owned-artifact-writer"), nil
}
func artifactOpen(parent *os.File, name string, directory, create bool) (*os.File, error) {
	text := C.CString(name)
	defer C.free(unsafe.Pointer(text))
	d, c := C.int(0), C.int(0)
	if directory {
		d = 1
	}
	if create {
		c = 1
	}
	fd, e := C.artifact_open(C.int(parent.Fd()), text, d, c)
	runtime.KeepAlive(parent)
	if fd < 0 {
		return nil, e
	}
	return os.NewFile(uintptr(fd), "owned-artifact"), nil
}
func artifactRename(parent *os.File, from, to string) error {
	a, b := C.CString(from), C.CString(to)
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(b))
	rc := C.artifact_rename(C.int(parent.Fd()), a, b)
	runtime.KeepAlive(parent)
	if rc != 0 {
		return syscall.Errno(rc)
	}
	return nil
}
func artifactUnlink(parent *os.File, name string, directory bool) error {
	text := C.CString(name)
	defer C.free(unsafe.Pointer(text))
	d := C.int(0)
	if directory {
		d = 1
	}
	rc := C.artifact_unlink(C.int(parent.Fd()), text, d)
	runtime.KeepAlive(parent)
	if rc != 0 {
		return syscall.Errno(rc)
	}
	return nil
}
