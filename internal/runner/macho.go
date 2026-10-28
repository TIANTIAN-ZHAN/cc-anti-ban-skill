package runner

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The supported layout is a native, little-endian, thin 64-bit executable.
// Parse raw commands: debug/macho leaves weak/reexport/upward dependencies as
// LoadBytes. Unknown commands and fat images are unsupported, never presumed safe.
// Command layouts: Apple's mach-o/loader.h (including dylib_use_command).
func checkNative(file *os.File) error {
	bad := errors.New("unsupported_native_layout")
	info, err := file.Stat()
	if err != nil || info.Size() < 32 || info.Size() > imageLimit {
		return bad
	}
	header := make([]byte, 32)
	if _, err = file.ReadAt(header, 0); err != nil {
		return bad
	}
	u32 := binary.LittleEndian.Uint32
	cpu := uint32(0x0100000c)
	if runtime.GOARCH == "amd64" {
		cpu = 0x01000007
	} else if runtime.GOARCH != "arm64" {
		return bad
	}
	if u32(header) != 0xfeedfacf || u32(header[4:]) != cpu || u32(header[12:]) != 2 {
		return bad
	}
	n, size := u32(header[16:]), u32(header[20:])
	if n == 0 || n > 8192 || size > 1<<20 || uint64(size)+32 > uint64(info.Size()) || uint64(n)*8 > uint64(size) {
		return bad
	}
	commands := make([]byte, size)
	if _, err = file.ReadAt(commands, 32); err != nil {
		return bad
	}
	within := func(offset, length uint64) bool {
		return offset <= uint64(info.Size()) && length <= uint64(info.Size())-offset
	}
	dylinker := false
	for i := uint32(0); i < n; i++ {
		if len(commands) < 8 {
			return bad
		}
		cmd, length := u32(commands), u32(commands[4:])
		if length < 8 || length%8 != 0 || uint64(length) > uint64(len(commands)) {
			return bad
		}
		raw := commands[:length]
		commands = commands[length:]
		stringAt := func(min uint32) (string, bool) {
			if len(raw) < 12 {
				return "", false
			}
			offset := u32(raw[8:])
			if offset < min || offset >= length {
				return "", false
			}
			rest := raw[offset:]
			end := bytes.IndexByte(rest, 0)
			if end <= 0 {
				return "", false
			}
			for _, b := range rest[end:] {
				if b != 0 {
					return "", false
				}
			}
			return string(rest[:end]), true
		}
		switch cmd {
		case 0xc, 0x80000018, 0x8000001f, 0x20, 0x80000023:
			if len(raw) < 24 {
				return bad
			}
			min := uint32(24)
			if u32(raw[12:]) == 0x1a741800 { // Extended dylib_use_command.
				if (cmd != 0xc && cmd != 0x80000018) || len(raw) < 28 || u32(raw[8:]) != 28 || u32(raw[24:]) & ^uint32(15) != 0 {
					return bad
				}
				min = 28
			}
			name, ok := stringAt(min)
			if !ok || filepath.Clean(name) != name || (!strings.HasPrefix(name, "/usr/lib/") && !strings.HasPrefix(name, "/System/Library/")) {
				return bad
			}
		case 0xe: // LC_LOAD_DYLINKER; Go's named constant 0xf means ID.
			name, ok := stringAt(12)
			if !ok || name != "/usr/lib/dyld" || dylinker {
				return bad
			}
			dylinker = true
		case 0x19: // segment_command_64 and section_64 array.
			if len(raw) < 72 || uint64(u32(raw[64:]))*80+72 != uint64(length) {
				return bad
			}
			if !within(binary.LittleEndian.Uint64(raw[40:]), binary.LittleEndian.Uint64(raw[48:])) {
				return bad
			}
			for j := 72; j < len(raw); j += 80 {
				section := raw[j : j+80]
				kind := u32(section[64:]) & 0xff
				if kind != 1 && kind != 0xc && kind != 0x12 && !within(uint64(u32(section[48:])), binary.LittleEndian.Uint64(section[40:])) {
					return bad
				}
				if !within(uint64(u32(section[56:])), uint64(u32(section[60:]))*8) {
					return bad
				}
			}
		case 2: // symtab_command
			if length != 24 || !within(uint64(u32(raw[8:])), uint64(u32(raw[12:]))*16) || !within(uint64(u32(raw[16:])), uint64(u32(raw[20:]))) {
				return bad
			}
		case 0xb: // dysymtab_command: six file-offset/count tables.
			if length != 80 {
				return bad
			}
			for j, width := range []uint64{8, 56, 4, 4, 8, 8} {
				start := 32 + j*8
				if !within(uint64(u32(raw[start:])), uint64(u32(raw[start+4:]))*width) {
					return bad
				}
			}
		case 0x22, 0x80000022: // dyld_info_command
			if length != 48 {
				return bad
			}
			for j := 8; j < 48; j += 8 {
				if !within(uint64(u32(raw[j:])), uint64(u32(raw[j+4:]))) {
					return bad
				}
			}
		case 0x1d, 0x1e, 0x26, 0x29, 0x2b, 0x2e, 0x80000033, 0x80000034:
			if length != 16 || !within(uint64(u32(raw[8:])), uint64(u32(raw[12:]))) {
				return bad
			}
		case 0x1b, 0x80000028:
			if length != 24 {
				return bad
			}
			if cmd == 0x80000028 && !within(binary.LittleEndian.Uint64(raw[8:]), 1) {
				return bad
			}
		case 0x24, 0x2a:
			if length != 16 {
				return bad
			}
		case 0x32:
			if length < 24 || uint64(u32(raw[20:]))*8+24 != uint64(length) {
				return bad
			}
		default:
			// Includes RPATH, DYLD_ENVIRONMENT, FVMLIB, FILESET, lazy-dylib
			// linkedit data and any future loading mechanism not examined here.
			return bad
		}
	}
	if len(commands) != 0 {
		return bad
	}
	return nil
}
