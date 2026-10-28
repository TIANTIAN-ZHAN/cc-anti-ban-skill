package runner

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func ownedFixture(t *testing.T) *Prepared {
	t.Helper()
	p, f, e := newPreparedFile()
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("owned")
	f.Close()
	t.Cleanup(func() {
		p.artifact.beforeCapture = nil
		p.artifact.afterCapture = nil
		if e := p.Close(); e != nil {
			t.Error("fixture recovery not drained", e)
		}
	})
	return p
}
func sentinelDir(t *testing.T, path string) {
	t.Helper()
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(path, "unrelated"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
}
func assertSentinel(t *testing.T, path string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(path, "unrelated"))
	if e != nil || string(b) != "keep" {
		t.Fatal("unrelated sentinel changed")
	}
}

func TestArtifactCleanupDirectorySubstitution(t *testing.T) {
	for _, when := range []string{"before_cleanup", "after_check", "restore_blocked"} {
		t.Run(when, func(t *testing.T) {
			p := ownedFixture(t)
			original := p.temporary
			moved := filepath.Join(t.TempDir(), "original")
			foreign := filepath.Join(t.TempDir(), "foreign")
			blocker := filepath.Join(t.TempDir(), "blocker")
			replace := func() {
				if e := os.Rename(original, moved); e != nil {
					t.Fatal(e)
				}
				sentinelDir(t, original)
			}
			if when == "before_cleanup" {
				replace()
			} else {
				p.artifact.beforeCapture = func(stage string) {
					if stage == "directory" {
						replace()
						p.artifact.beforeCapture = nil
					}
				}
				if when == "restore_blocked" {
					p.artifact.afterCapture = func(stage string) {
						if stage == "directory" {
							sentinelDir(t, original)
							p.artifact.afterCapture = nil
						}
					}
				}
			}
			if e := p.Close(); e == nil {
				t.Fatal("substituted directory reported cleaned")
			}
			if _, e := os.Stat(filepath.Join(moved, "entry")); e != nil {
				t.Fatal("owned object lost")
			}
			if when == "restore_blocked" {
				if p.CleanupCode() != "cleanup_restore_pending" || p.artifact.undo == nil {
					t.Fatal("restore failure not retained")
				}
				assertSentinel(t, original)
				if e := p.Close(); e == nil || p.CleanupCode() != "cleanup_restore_pending" {
					t.Fatal("blocked restore cleared")
				}
				os.Rename(original, blocker)
				if e := p.Close(); e == nil || p.artifact.undo != nil {
					t.Fatal("restoration not separately acknowledged")
				}
				assertSentinel(t, blocker)
			}
			assertSentinel(t, original)
			os.Rename(original, foreign)
			os.Rename(moved, original)
			if e := p.Close(); e != nil {
				t.Fatal("original identity could not recover", e)
			}
			assertSentinel(t, foreign)
			// Repeated cleanup must not touch a new object reusing the original name.
			sentinelDir(t, original)
			defer os.RemoveAll(original)
			if e := p.Close(); e != nil {
				t.Fatal(e)
			}
			assertSentinel(t, original)
		})
	}
}

func TestArtifactCleanupLeafSubstitutionAndUnexpectedContents(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "extra"} {
		t.Run(kind, func(t *testing.T) {
			p := ownedFixture(t)
			foreign := filepath.Join(t.TempDir(), "unrelated")
			os.WriteFile(foreign, []byte("keep"), 0600)
			oldEntry := filepath.Join(p.temporary, "entry")
			saved := filepath.Join(t.TempDir(), "owned")
			if kind == "extra" {
				os.WriteFile(filepath.Join(p.temporary, "other"), []byte("keep"), 0600)
			} else {
				p.artifact.beforeCapture = func(stage string) {
					if stage == "entry" {
						// Root is already captured, so use its current task-owned location.
						current := filepath.Join(filepath.Dir(p.temporary), p.artifact.base)
						oldEntry = filepath.Join(current, "entry")
						os.Rename(oldEntry, saved)
						if kind == "symlink" {
							os.Symlink(foreign, oldEntry)
						} else {
							os.WriteFile(oldEntry, []byte("foreign"), 0600)
						}
						p.artifact.beforeCapture = nil
					}
				}
			}
			if p.Close() == nil {
				t.Fatal("foreign content deleted")
			}
			b, _ := os.ReadFile(foreign)
			if string(b) != "keep" {
				t.Fatal("symlink target changed")
			}
			current := filepath.Join(filepath.Dir(p.temporary), p.artifact.base)
			if kind == "extra" {
				os.Rename(filepath.Join(current, "other"), filepath.Join(t.TempDir(), "preserved"))
			} else {
				os.Rename(oldEntry, filepath.Join(t.TempDir(), "foreign-entry"))
				os.Rename(saved, oldEntry)
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if e := p.Close(); e != nil {
						t.Error(e)
					}
				}()
			}
			wg.Wait()
			if !p.removed {
				t.Fatal("restored object not cleaned")
			}
		})
	}
}

func TestArtifactRemovalErrorDoesNotDiscardDescriptors(t *testing.T) {
	p := ownedFixture(t)
	p.remove = func(string) error { return errors.New("fixture fails") }
	if p.Close() == nil || p.artifact.parent == nil || p.artifact.directory == nil || p.artifact.entry == nil {
		t.Fatal("ownership dropped")
	}
	p.remove = nil
	if e := p.Close(); e != nil {
		t.Fatal(e)
	}
}
