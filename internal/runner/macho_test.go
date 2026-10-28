package runner

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func machoFixture(commands ...[]byte) []byte {
	b := make([]byte, 32)
	u := binary.LittleEndian.PutUint32
	u(b, 0xfeedfacf)
	cpu := uint32(0x0100000c)
	if runtime.GOARCH == "amd64" {
		cpu = 0x01000007
	}
	u(b[4:], cpu)
	u(b[12:], 2)
	u(b[16:], uint32(len(commands)))
	for _, c := range commands {
		b = append(b, c...)
	}
	u(b[20:], uint32(len(b)-32))
	return b
}
func dylibFixture(cmd uint32, name string) []byte {
	b := make([]byte, (24+len(name)+1+7)&^7)
	u := binary.LittleEndian.PutUint32
	u(b, cmd)
	u(b[4:], uint32(len(b)))
	u(b[8:], 24)
	copy(b[24:], name)
	return b
}
func TestRawMachOLoadCommands(t *testing.T) {
	check := func(t *testing.T, b []byte, want bool) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "synthetic")
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		f, e := os.Open(p)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		if got := checkNative(f) == nil; got != want {
			t.Fatalf("accepted=%v want=%v", got, want)
		}
	}
	for _, cmd := range []uint32{0xc, 0x80000018, 0x8000001f, 0x20, 0x80000023} {
		for _, name := range []string{"/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/Foundation.framework/Versions/C/Foundation", "/private/tmp/external.dylib", "@rpath/external.dylib", "relative.dylib", "/usr/lib/../private/external.dylib", "/usr/library/fake", "/usr/lib//fake"} {
			t.Run(name, func(t *testing.T) {
				check(t, machoFixture(dylibFixture(cmd, name)), name == "/usr/lib/libSystem.B.dylib" || name == "/System/Library/Frameworks/Foundation.framework/Versions/C/Foundation")
			})
		}
	}
	base := func() []byte { return machoFixture(dylibFixture(0xc, "/usr/lib/libSystem.B.dylib")) }
	u := binary.LittleEndian.PutUint32
	mutations := map[string]func([]byte) []byte{
		"unknown": func(b []byte) []byte { u(b[32:], 0x777); return b }, "required_unknown": func(b []byte) []byte { u(b[32:], 0x80000777); return b },
		"rpath": func(b []byte) []byte { u(b[32:], 0x8000001c); return b }, "dyld_env": func(b []byte) []byte { u(b[32:], 0x27); return b }, "lazy_info": func(b []byte) []byte { u(b[32:], 0x3a); return b },
		"header_offset": func(b []byte) []byte { u(b[40:], 8); return b }, "outside_offset": func(b []byte) []byte { u(b[40:], 999); return b }, "unterminated": func(b []byte) []byte {
			for i := 56; i < len(b); i++ {
				b[i] = 'x'
			}
			return b
		},
		"short": func(b []byte) []byte { return b[:38] }, "zero_cmdsize": func(b []byte) []byte { u(b[36:], 0); return b }, "outside_size": func(b []byte) []byte { u(b[36:], 4096); return b }, "unaligned": func(b []byte) []byte { u(b[36:], 25); return b },
		"count_mismatch": func(b []byte) []byte { u(b[16:], 2); return b }, "leftover": func(b []byte) []byte { b = append(b, make([]byte, 8)...); u(b[20:], uint32(len(b)-32)); return b }, "commands_overflow": func(b []byte) []byte { u(b[20:], 0xffffffff); return b },
		"fat32": func(b []byte) []byte { u(b, 0xbebafeca); return b }, "fat64": func(b []byte) []byte { u(b, 0xbfbafeca); return b }, "fat_swap": func(b []byte) []byte { u(b, 0xcafebabe); return b }, "fat64_swap": func(b []byte) []byte { u(b, 0xcafebabf); return b },
		"foreign_cpu": func(b []byte) []byte { u(b[4:], 0); return b }, "not_exec": func(b []byte) []byte { u(b[12:], 6); return b },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) { check(t, mutate(base()), false) })
	}
	for _, name := range []string{"/usr/lib/dyld", "/private/tmp/dyld"} {
		c := dylibFixture(0xe, name)
		check(t, machoFixture(c), name == "/usr/lib/dyld")
		check(t, machoFixture(c, c), false)
	}
	extended := make([]byte, 64)
	u(extended, 0x80000018)
	u(extended[4:], 64)
	u(extended[8:], 28)
	u(extended[12:], 0x1a741800)
	u(extended[24:], 15)
	copy(extended[28:], "/usr/lib/libSystem.B.dylib")
	check(t, machoFixture(extended), true)
	u(extended[24:], 16)
	check(t, machoFixture(extended), false)
	segment := make([]byte, 72)
	u(segment, 0x19)
	u(segment[4:], 72)
	binary.LittleEndian.PutUint64(segment[40:], 1<<63)
	check(t, machoFixture(segment), false)
}

func TestMaterializeFailureReturnsOwnedCleanupTarget(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	i, e := OpenImage(context.Background(), path, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer i.Close()
	i.Consume()
	i.Digest = "changed"
	i.remove = func(string) error { return errors.New("fixture denied") }
	p, e := i.Materialize(context.Background())
	if e == nil || p == nil {
		t.Fatal("cleanup target lost on materialize error")
	}
	if _, e = os.Stat(p.temporary); e != nil {
		t.Fatal("retained artifact disappeared")
	}
	p.remove = nil
	if p.Close() != nil {
		t.Fatal("retry")
	}
	// A successful retry is permanently idempotent even if its old name is reused.
	if e = os.Mkdir(p.temporary, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(p.temporary)
	sentinel := filepath.Join(p.temporary, "unrelated")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	if p.Close() != nil {
		t.Fatal("repeat")
	}
	if _, e = os.Stat(sentinel); e != nil {
		t.Fatal("repeated cleanup removed a replacement")
	}
}
