//go:build darwin

package main

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// Exercise the actual README blocks and build-safe script with isolated HOME,
// paths and harmless native fixtures. No real Claude, config or network is used.
func TestReadmeConditionalBuildStartsChosenProject(t *testing.T) {
	block := readmeZshBlock(t, "下面整段从工具仓库根目录运行。")
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-safe.zsh"))
	if err != nil {
		t.Fatal(err)
	}
	oldBinary, fresh := readmeNativeFixtures(t)
	for _, mode := range []string{"fail", "success", "empty", "relative", "missing", "eof"} {
		t.Run(mode, func(t *testing.T) {
			for _, state := range []string{"preset", "unset", "mixed"} {
				t.Run(state, func(t *testing.T) {
					root := t.TempDir()
					env := readmeFixtureEnv(t, root)
					fakeRepo := filepath.Join(root, "repository with spaces")
					project := filepath.Join(root, "chosen project with spaces")
					for _, p := range []string{filepath.Join(fakeRepo, "scripts"), filepath.Join(fakeRepo, ".build"), project} {
						if err := os.MkdirAll(p, 0700); err != nil {
							t.Fatal(err)
						}
					}
					readmeWrite(t, filepath.Join(fakeRepo, "scripts", "build-safe.zsh"), script, 0600)
					old := filepath.Join(fakeRepo, ".build", "cc-health")
					readmeWrite(t, old, oldBinary, 0700)
					fakeGo := `#!/bin/sh
if [ "$CC_BUILD_MODE" = fail ]; then exit 9; fi
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then output=$2; shift; fi
  shift
done
[ -n "$output" ] || exit 10
/bin/cp "$CC_NEW_BINARY" "$output"
`
					readmeWrite(t, filepath.Join(root, "bin", "go"), []byte(fakeGo), 0700)
					input := project + "\n"
					switch mode {
					case "empty":
						input = "\n"
					case "relative":
						input = "../chosen project with spaces\n"
					case "missing":
						input = filepath.Join(root, "missing project") + "\n"
					case "eof":
						input = ""
					}
					receipt := filepath.Join(root, "launch-receipt")
					cmd := exec.Command("/bin/zsh", "-f", "-c", readmeParentShell(block, state))
					cmd.Dir = fakeRepo
					cmd.Stdin = strings.NewReader(input)
					cmd.Env = append(env, "CC_BUILD_MODE="+mode, "CC_NEW_BINARY="+fresh, "CC_LAUNCH_RECEIPT="+receipt)
					out, runErr := cmd.CombinedOutput()
					readmeParentPreserved(t, out)
					if mode == "success" {
						if runErr != nil {
							t.Fatalf("successful build/start: %v %s", runErr, out)
						}
						data, err := os.ReadFile(receipt)
						if err != nil {
							t.Fatal(err)
						}
						physical, err := filepath.EvalSymlinks(project)
						if err != nil {
							t.Fatal(err)
						}
						if string(data) != "fresh\n"+physical+"\nserve\n" {
							t.Fatal("wrong executable, arguments or project cwd")
						}
					} else {
						if runErr == nil {
							t.Fatal("rejected build/input reported success")
						}
						if _, err := os.Stat(receipt); !os.IsNotExist(err) {
							t.Fatal("rejected build/input launched an executable")
						}
					}
					if mode == "fail" {
						after, err := os.ReadFile(old)
						if err != nil || sha256.Sum256(oldBinary) != sha256.Sum256(after) {
							t.Fatal("failed build replaced old executable")
						}
					}
				})
			}
		})
	}
}

func TestReadmeSkillInstallPreservesExistingTargets(t *testing.T) {
	block := readmeZshBlock(t, "## 可选 Skill 安装")
	for _, kind := range []string{"missing_parents", "missing_target", "file", "directory", "directory_symlink", "file_symlink", "dangling_symlink", "parent_file"} {
		t.Run(kind, func(t *testing.T) {
			for _, state := range []string{"preset", "unset", "mixed"} {
				t.Run(state, func(t *testing.T) {
					root := t.TempDir()
					env := readmeFixtureEnv(t, root)
					fakeRepo := filepath.Join(root, "repository with spaces")
					referent := filepath.Join(root, "existing directory")
					for _, p := range []string{fakeRepo, referent} {
						if err := os.MkdirAll(p, 0700); err != nil {
							t.Fatal(err)
						}
					}
					readmeWrite(t, filepath.Join(referent, "keep.txt"), []byte("keep existing contents\n"), 0600)
					target := filepath.Join(root, "home", ".agents", "skills", "cc-anti-ban-skill")
					if kind != "missing_parents" {
						if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
							t.Fatal(err)
						}
					}
					switch kind {
					case "file":
						readmeWrite(t, target, []byte("existing skill file\n"), 0640)
					case "directory":
						if err := os.Mkdir(target, 0700); err != nil {
							t.Fatal(err)
						}
						readmeWrite(t, filepath.Join(target, "keep.txt"), []byte("existing skill directory\n"), 0600)
					case "directory_symlink", "file_symlink", "dangling_symlink":
						link := referent
						if kind == "file_symlink" {
							link = filepath.Join(referent, "keep.txt")
						} else if kind == "dangling_symlink" {
							link = filepath.Join(root, "not present")
						}
						if err := os.Symlink(link, target); err != nil {
							t.Fatal(err)
						}
					case "parent_file":
						if err := os.Remove(filepath.Dir(target)); err != nil {
							t.Fatal(err)
						}
						readmeWrite(t, filepath.Dir(target), []byte("parent is a file\n"), 0600)
					}
					before := readmeTree(t, root)
					run := func() error {
						cmd := exec.Command("/bin/zsh", "-f", "-c", readmeParentShell(block, state))
						cmd.Dir, cmd.Env = fakeRepo, env
						out, err := cmd.CombinedOutput()
						readmeParentPreserved(t, out)
						return err
					}
					err := run()
					if kind == "missing_parents" || kind == "missing_target" {
						if err != nil {
							t.Fatalf("new Skill link: %v", err)
						}
						link, err := os.Readlink(target)
						physical, physicalErr := filepath.EvalSymlinks(fakeRepo)
						if err != nil || physicalErr != nil || link != physical {
							t.Fatalf("wrong Skill link: %q, %v, %v", link, err, physicalErr)
						}
						before = readmeTree(t, root)
						if err := run(); err == nil {
							t.Fatal("repeated installation reported success")
						}
					} else if err == nil {
						t.Fatal("existing target/invalid parent reported success")
					}
					if after := readmeTree(t, root); !reflect.DeepEqual(before, after) {
						t.Fatal("rejected installation changed an object, its contents or directory entries")
					}
				})
			}
		})
	}
}

func readmeZshBlock(t *testing.T, marker string) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	if strings.Count(text, marker) != 1 {
		t.Fatalf("README block marker missing or ambiguous: %s", marker)
	}
	text = text[strings.Index(text, marker)+len(marker):]
	const fence = "```zsh\n"
	start := strings.Index(text, fence)
	if start < 0 {
		t.Fatal("README zsh block missing")
	}
	text = text[start+len(fence):]
	end := strings.Index(text, "\n```")
	if end < 0 {
		t.Fatal("README code fence missing")
	}
	return text[:end]
}

func readmeParentShell(block, state string) string {
	setup := "cc_readme_test_cwd=$PWD\n"
	checks := "[[ $PWD == \"$cc_readme_test_cwd\" ]] || exit 91\n"
	for i, name := range []string{"cc_health_repo", "cc_health_project", "cc_health_skill_target"} {
		if state == "unset" || (state == "mixed" && i == 1) {
			setup += "unset -- " + name + "\n"
			checks += "[[ ${+" + name + "} == 0 ]] || exit 92\n"
		} else {
			value := "original value with spaces"
			if state == "mixed" && i == 0 {
				value = ""
			}
			setup += name + "='" + value + "'\n"
			checks += "[[ ${+" + name + "} == 1 && $" + name + " == '" + value + "' ]] || exit 93\n"
		}
	}
	return setup + block + "\ncc_readme_test_status=$?\n" + checks + "print -r -- PARENT_STATE_PRESERVED\nexit $cc_readme_test_status\n"
}

func readmeParentPreserved(t *testing.T, out []byte) {
	t.Helper()
	if !strings.HasSuffix(string(out), "PARENT_STATE_PRESERVED\n") {
		t.Fatalf("README block did not preserve parent variables/cwd or return to parent: %s", out)
	}
}

func readmeFixtureEnv(t *testing.T, root string) []string {
	t.Helper()
	for _, name := range []string{"home", "tmp", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"PATH=" + filepath.Join(root, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp")}
}

func readmeNativeFixtures(t *testing.T) ([]byte, string) {
	t.Helper()
	root := t.TempDir()
	env := readmeFixtureEnv(t, root)
	for _, tag := range []string{"old", "fresh"} {
		source := filepath.Join(root, tag+".c")
		code := `#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
int main(int argc,char **argv) {
 char cwd[8192]; const char *receipt=getenv("CC_LAUNCH_RECEIPT");
 if (!receipt || !getcwd(cwd,sizeof(cwd))) return 80;
 FILE *f=fopen(receipt,"w"); if(!f)return 81;
 fprintf(f,"` + tag + `\n%s\n%s\n",cwd,argc>1?argv[1]:"");
 return fclose(f)==0 ? 0 : 82;
}`
		readmeWrite(t, source, []byte(code), 0600)
		cmd := exec.Command("/usr/bin/clang", "-mmacosx-version-min=13.0", source, "-o", filepath.Join(root, tag))
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fake executable build: %v %s", err, out)
		}
	}
	old, err := os.ReadFile(filepath.Join(root, "old"))
	if err != nil {
		t.Fatal(err)
	}
	return old, filepath.Join(root, "fresh")
}

func readmeWrite(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

type readmeTreeEntry struct {
	mode    fs.FileMode
	inode   uint64
	mtime   int64
	content string
}

func readmeTree(t *testing.T, root string) map[string]readmeTreeEntry {
	t.Helper()
	result := make(map[string]readmeTreeEntry)
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		e := readmeTreeEntry{mode: info.Mode(), inode: info.Sys().(*syscall.Stat_t).Ino, mtime: info.ModTime().UnixNano()}
		if info.Mode()&os.ModeSymlink != 0 {
			e.content, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var data []byte
			data, err = os.ReadFile(path)
			e.content = string(data)
		}
		result[path] = e
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
