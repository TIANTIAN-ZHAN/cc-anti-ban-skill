//go:build darwin

package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cc-anti-ban-skill/internal/execution"
	"cc-anti-ban-skill/internal/health"
	"cc-anti-ban-skill/internal/runner"
)

type cleanupFixture struct {
	server                            *httptest.Server
	handler                           *Handler
	manager                           *execution.Manager
	bootstrap                         string
	calls                             atomic.Int32
	started                           atomic.Bool
	expired                           atomic.Bool
	deny                              atomic.Bool
	clock                             atomic.Int64
	identity                          bool
	tempDir, original, moved, foreign string
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	f := &cleanupFixture{bootstrap: Token()}
	f.clock.Store(time.Now().Unix())
	f.deny.Store(true)
	f.tempDir = t.TempDir()
	t.Setenv("TMPDIR", f.tempDir)
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	os.Mkdir(dir, 0700)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	now := func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() }
	d := health.LocalIO{OS: "darwin", Home: home, Directory: dir, Env: map[string]string{}, Lookup: func(string) (string, error) { return exe, nil }, Version: func(context.Context, string) (string, error) { return "2.1.999 (Claude Code)", nil }, Stat: func(p string) (fs.FileInfo, error) {
		if strings.HasPrefix(p, home+"/") {
			return os.Lstat(p)
		}
		return nil, fs.ErrNotExist
	}, Now: now}
	f.manager = execution.New(context.Background(), execution.Dependencies{
		Recheck: func(ctx context.Context, s health.Snapshot) health.Snapshot { return health.Reinspect(ctx, d, s) }, Available: func() bool { return true },
		Start: func(*runner.Prepared, []string) (execution.Process, error) {
			f.started.Store(true)
			return nil, errors.New("fixture native spawn failed")
		},
		Cleanup: func(p *runner.Prepared) error {
			attempt := f.calls.Add(1)
			if f.identity {
				if attempt == 1 {
					matches, _ := filepath.Glob(filepath.Join(f.tempDir, "cc-health-approved-*"))
					if len(matches) != 1 {
						t.Error("fixture artifact count")
						return errors.New("fixture")
					}
					f.original = matches[0]
					f.moved = filepath.Join(f.tempDir, "held-original")
					f.foreign = filepath.Join(f.tempDir, "preserved-foreign")
					if os.Rename(f.original, f.moved) != nil || os.Mkdir(f.original, 0700) != nil || os.WriteFile(filepath.Join(f.original, "unrelated"), []byte("keep"), 0600) != nil {
						t.Error("substitution fixture")
						return errors.New("fixture")
					}
				} else if attempt == 3 {
					if os.Rename(f.original, f.foreign) != nil || os.Rename(f.moved, f.original) != nil {
						t.Error("restore fixture")
						return errors.New("fixture")
					}
				}
				return p.Close()
			}
			if f.deny.Load() {
				return errors.New("fixture cleanup denied /private/path-must-not-escape")
			}
			return p.Close()
		},
		Environment: func() []string { return []string{"HOME=" + home, "FAKE_SECRET=not-real"} }, Network: func() health.NetworkIO { t.Error("unexpected reference network"); return health.NetworkIO{} }, Now: now,
	})
	f.handler = New("pending", f.bootstrap, Actions{Scan: func(ctx context.Context) health.Snapshot { return health.Inspect(ctx, d) }, Plan: func(s health.Snapshot, n time.Time) health.Plan { return s.Plan(n) }, Probe: func(context.Context, health.Snapshot, health.Plan) health.Report {
		t.Error("unexpected probe")
		return health.Report{}
	}, Close: func() { f.expired.Store(true) }, Now: now, Execution: f.manager})
	f.server = httptest.NewUnstartedServer(f.handler)
	f.handler.host = f.server.Listener.Addr().String()
	f.handler.origin = "http://" + f.handler.host
	f.server.Start()
	t.Cleanup(func() {
		f.deny.Store(false)
		id := f.manager.CurrentID()
		f.manager.Close()
		deadline := time.Now().Add(2 * time.Second)
		for f.manager.ResourcesPending() && time.Now().Before(deadline) {
			f.manager.RetryCleanup(id)
			time.Sleep(time.Millisecond)
		}
		if f.manager.ResourcesPending() {
			t.Error("fixture retained resources")
		}
		f.server.Close()
	})
	return f
}
func (f *cleanupFixture) request(t *testing.T, session, path, body, alter string) (int, []byte) {
	t.Helper()
	r, e := http.NewRequest("POST", f.server.URL+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Session", session)
	if path == "/api/session" {
		r.Header.Set("X-Bootstrap", f.bootstrap)
	}
	switch alter {
	case "host":
		r.Host = "attacker.invalid"
	case "origin":
		r.Header.Set("Origin", "https://attacker.invalid")
	case "session":
		r.Header.Set("X-Session", "wrong")
	case "missing-origin":
		r.Header.Del("Origin")
	}
	response, e := f.server.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	b, _ := io.ReadAll(response.Body)
	return response.StatusCode, b
}
func TestCleanupHTTPRetainsAuthenticatedRecoveryAfterExpiry(t *testing.T) {
	for _, cause := range []string{"deadline", "close"} {
		t.Run(cause, func(t *testing.T) {
			f := newCleanupFixture(t)
			_, b := f.request(t, "", "/api/session", "{}", "")
			var login map[string]string
			json.Unmarshal(b, &login)
			session := login["session"]
			if c, _ := f.request(t, session, "/api/scan", "{}", ""); c != 200 {
				t.Fatal("scan")
			}
			_, b = f.request(t, session, "/api/repair-plan", `{"loopback":"127.0.0.1","port":4567}`, "")
			var p execution.Plan
			json.Unmarshal(b, &p)
			if !p.Allowed {
				t.Fatal("plan", p.Reason)
			}
			body := fmt.Sprintf(`{"plan_id":%q}`, p.ID)
			if c, _ := f.request(t, session, "/api/repair-start", body, ""); c != 200 {
				t.Fatal("start")
			}
			var state execution.Status
			until := time.Now().Add(2 * time.Second)
			for time.Now().Before(until) {
				state, _ = f.manager.Status(p.ID)
				if state.Finished {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !state.Finished || !state.ArtifactRetained || !state.CleanupRetryAllowed {
				t.Fatal("cleanup target missing")
			}
			if c, _ := f.request(t, session, "/api/repair-plan", `{"loopback":"127.0.0.1","port":4567}`, ""); c != 409 {
				t.Fatal("new plan replaced target")
			}
			if cause == "deadline" {
				f.clock.Add(16 * 60)
				f.handler.Expire()
			} else {
				c, b := f.request(t, session, "/api/close", "{}", "")
				if c != 200 || !strings.Contains(string(b), `"cleanup_only":true`) {
					t.Fatal("close discarded target")
				}
			}
			for _, path := range []string{"/api/scan", "/api/plan", "/api/repair-plan", "/api/repair-start", "/api/probe", "/api/repair-stop"} {
				body := "{}"
				if path == "/api/repair-plan" {
					body = `{"loopback":"127.0.0.1","port":4567}`
				}
				if strings.HasSuffix(path, "start") || strings.HasSuffix(path, "stop") || path == "/api/probe" {
					body = fmt.Sprintf(`{"plan_id":%q}`, p.ID)
				}
				if c, _ := f.request(t, session, path, body, ""); c != 410 {
					t.Fatalf("expired action %s remained reachable: %d", path, c)
				}
			}
			for _, alter := range []string{"host", "origin", "session", "missing-origin"} {
				if c, _ := f.request(t, session, "/api/repair-cleanup", body, alter); c != 403 {
					t.Fatal("cleanup auth bypass", alter)
				}
			}
			if c, _ := f.request(t, session, "/api/repair-cleanup", `{"plan_id":"foreign"}`, ""); c != 409 {
				t.Fatal("foreign nonce")
			}
			for _, extra := range []string{`"path":"/private/other"`, `"pid":123`} {
				if c, _ := f.request(t, session, "/api/repair-cleanup", fmt.Sprintf(`{"plan_id":%q,%s}`, p.ID, extra), ""); c != 400 {
					t.Fatal("arbitrary resource accepted")
				}
			}
			if c, b := f.request(t, session, "/api/repair-status", body, ""); c != 200 || strings.Contains(string(b), "private/path") {
				t.Fatal("status unavailable or leaks")
			}
			c, b := f.request(t, session, "/api/repair-cleanup", body, "")
			json.Unmarshal(b, &state)
			if c != 200 || !state.ArtifactRetained || !state.CleanupOnly {
				t.Fatal("failed retry lost original session")
			}
			f.deny.Store(false)
			before := f.calls.Load()
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c, b := f.request(t, session, "/api/repair-cleanup", body, "")
					var s execution.Status
					json.Unmarshal(b, &s)
					if c != 200 || s.ArtifactRetained {
						t.Error("concurrent retry")
					}
				}()
			}
			wg.Wait()
			if f.calls.Load() != before+1 || f.manager.ResourcesPending() {
				t.Fatal("retry repeated or lost ownership")
			}
			if c, b := f.request(t, session, "/api/close", "{}", ""); c != 200 || !strings.Contains(string(b), `"closed":true`) {
				t.Fatal("clean close")
			}
		})
	}
}

func TestCleanupUIFixture(t *testing.T) {
	path := os.Getenv("CC_HEALTH_CLEANUP_UI_FIXTURE")
	if path == "" {
		t.Skip("manual isolated cleanup UI")
	}
	if tmp := os.Getenv("TMPDIR"); tmp == "" || !strings.HasPrefix(filepath.Clean(path), filepath.Clean(tmp)+"/") {
		t.Fatal("fixture output outside TMPDIR")
	}
	f := newCleanupFixture(t)
	f.identity = os.Getenv("CC_HEALTH_CLEANUP_IDENTITY") == "1"
	os.WriteFile(path, []byte(f.server.URL+"/#"+f.bootstrap), 0600)
	defer os.Remove(path)
	deadline := time.Now().Add(3 * time.Minute)
	expired := false
	for time.Now().Before(deadline) {
		if f.started.Load() && !expired {
			f.clock.Add(16 * 60)
			f.handler.Expire()
			expired = true
		}
		// First user retry demonstrates the failure; second may actually remove.
		if f.calls.Load() >= 2 {
			f.deny.Store(false)
		}
		if expired && !f.manager.ResourcesPending() {
			time.Sleep(300 * time.Millisecond)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	id := f.manager.CurrentID()
	s, _ := f.manager.Status(id)
	raw, _ := json.Marshal(map[string]any{"status": s, "cleanup_attempts": f.calls.Load(), "expired": expired})
	os.WriteFile(path+".result.json", raw, 0600)
	if f.identity {
		b, e := os.ReadFile(filepath.Join(f.foreign, "unrelated"))
		if e != nil || string(b) != "keep" {
			t.Fatal("unrelated sentinel changed")
		}
	}
	if !expired || s.ArtifactRetained || f.calls.Load() != 3 {
		t.Fatal("UI cleanup workflow incomplete")
	}
}

func TestCleanupIdentityHTTPKeepsForeignDirectory(t *testing.T) {
	f := newCleanupFixture(t)
	f.identity = true
	_, b := f.request(t, "", "/api/session", "{}", "")
	var login map[string]string
	json.Unmarshal(b, &login)
	session := login["session"]
	f.request(t, session, "/api/scan", "{}", "")
	_, b = f.request(t, session, "/api/repair-plan", `{"loopback":"127.0.0.1","port":4567}`, "")
	var p execution.Plan
	json.Unmarshal(b, &p)
	if !p.Allowed {
		t.Fatal(p.Reason)
	}
	body := fmt.Sprintf(`{"plan_id":%q}`, p.ID)
	f.request(t, session, "/api/repair-start", body, "")
	var state execution.Status
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		state, _ = f.manager.Status(p.ID)
		if state.Finished {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !state.ArtifactRetained || state.CleanupCode != "cleanup_identity_changed" {
		t.Fatal("identity issue not retained", state.CleanupCode)
	}
	f.handler.Expire()
	for expected := int32(2); expected <= 3; expected++ {
		code, b := f.request(t, session, "/api/repair-cleanup", body, "")
		json.Unmarshal(b, &state)
		if code != 200 || state.ArtifactRetained != (expected == 2) || f.calls.Load() != expected {
			t.Fatal("identity retry")
		}
	}
	bytes, e := os.ReadFile(filepath.Join(f.foreign, "unrelated"))
	if e != nil || string(bytes) != "keep" {
		t.Fatal("foreign sentinel changed")
	}
	if f.manager.ResourcesPending() {
		t.Fatal("owned object not released")
	}
}
