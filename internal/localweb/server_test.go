package localweb

import (
	"bytes"
	"context"
	"encoding/json"
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

	"cc-anti-ban-skill/internal/health"
)

type fixture struct {
	h                            *Handler
	bootstrap, session           string
	scans, plans, probes, closes atomic.Int32
	now                          time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{bootstrap: Token(), now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	a := Actions{Now: func() time.Time { return f.now }, Scan: func(ctx context.Context) health.Snapshot {
		f.scans.Add(1)
		return health.Inspect(ctx, health.LocalIO{OS: "darwin", Home: "/fixture/PRIVATE_HOME", Directory: "/fixture/PRIVATE_DIRECTORY", Env: map[string]string{}, Lookup: func(string) (string, error) { return "/fixture/PRIVATE_BIN", nil }, Version: func(context.Context, string) (string, error) { return "2.1.999 (Claude Code)", nil }, Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }, ResolverConfig: func() ([]byte, error) { return []byte("nameserver 192.0.2.53\n"), nil }, Now: func() time.Time { return f.now }})
	}, Plan: func(s health.Snapshot, now time.Time) health.Plan { f.plans.Add(1); return s.Plan(now) }, Probe: func(ctx context.Context, s health.Snapshot, p health.Plan) health.Report {
		f.probes.Add(1)
		return s.Report
	}, Close: func() { f.closes.Add(1) }}
	f.h = New("127.0.0.1:43210", f.bootstrap, a)
	return f
}

func (f *fixture) request(path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Session", f.session)
	if path == "/api/session" {
		r.Header.Set("X-Bootstrap", f.bootstrap)
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) login(t *testing.T) {
	t.Helper()
	w := f.request("/api/session", "{}", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var result map[string]string
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("session decode")
	}
	f.session = result["session"]
}

func TestAuthenticationAndOriginMatrix(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*http.Request)
		code   int
	}{
		{"missing Origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"foreign Origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.invalid") }, 403},
		{"null Origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"wrong Host port", func(r *http.Request) { r.Host = "127.0.0.1:43211" }, 403},
		{"DNS rebinding Host", func(r *http.Request) { r.Host = "attacker.invalid:43210" }, 403},
		{"localhost alias", func(r *http.Request) { r.Host = "localhost:43210" }, 403},
		{"wrong bootstrap", func(r *http.Request) { r.Header.Set("X-Bootstrap", "wrong") }, 403},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"form", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 415},
		{"method", func(r *http.Request) { r.Method = "GET" }, 405},
		{"query token", func(r *http.Request) { r.URL.RawQuery = "token=anything" }, 403},
		{"encoded path", func(r *http.Request) { r.URL.RawPath = "/api/%73ession" }, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t)
			w := f.request("/api/session", "{}", tt.change)
			if w.Code != tt.code {
				t.Fatalf("got %d", w.Code)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" || f.scans.Load() != 0 {
				t.Fatal("unsafe response")
			}
		})
	}
	f := setup(t)
	f.login(t)
	if f.request("/api/session", "{}", nil).Code != 403 {
		t.Fatal("bootstrap reused")
	}
	if f.request("/api/scan", "{}", func(r *http.Request) { r.Header.Del("Origin") }).Code != 403 || f.scans.Load() != 0 {
		t.Fatal("sensitive read missing Origin allowed")
	}
	if f.request("/api/scan", "{}", func(r *http.Request) {
		r.Header.Del("X-Session")
		r.AddCookie(&http.Cookie{Name: "session", Value: f.session})
	}).Code != 403 {
		t.Fatal("cookie fallback accepted")
	}
	for _, body := range []string{`{"target":"https://evil.invalid"}`, `{} {}`, `{"plan_id":"wrong"}`, strings.Repeat(" ", 257) + `{}`} {
		if f.request("/api/scan", body, nil).Code != 400 {
			t.Fatal("unbounded or extra input accepted")
		}
	}
	w := f.request("/api/scan", "{}", nil)
	if w.Code != 200 || f.scans.Load() != 1 {
		t.Fatal("valid local scan rejected")
	}
	for _, secret := range []string{"PRIVATE_HOME", "PRIVATE_DIRECTORY", "PRIVATE_BIN", "192.0.2.53", f.bootstrap, f.session} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("response leaked input")
		}
	}
}

func planID(t *testing.T, f *fixture) string {
	t.Helper()
	w := f.request("/api/plan", "{}", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var p health.Plan
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || !p.Allowed {
		t.Fatal("bad plan")
	}
	return p.ID
}

func TestConsentLifecycleSingleUseAndConcurrency(t *testing.T) {
	f := setup(t)
	f.login(t)
	if f.request("/api/probe", `{"plan_id":"invented"}`, nil).Code != 409 {
		t.Fatal("unplanned probe accepted")
	}
	if f.request("/api/plan", "{}", nil).Code != 409 {
		t.Fatal("unscanned plan accepted")
	}
	if f.request("/api/scan", "{}", nil).Code != 200 {
		t.Fatal("scan failed")
	}
	id := planID(t, f)
	if f.request("/api/probe", `{"plan_id":"wrong"}`, nil).Code != 409 {
		t.Fatal("wrong plan accepted")
	}
	if f.probes.Load() != 0 {
		t.Fatal("preview contacted network")
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- f.request("/api/probe", fmt.Sprintf(`{"plan_id":%q}`, id), nil).Code }()
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if success != 1 || f.probes.Load() != 1 {
		t.Fatal("plan replayed")
	}
	id = planID(t, f)
	f.now = f.now.Add(61 * time.Second)
	if f.request("/api/probe", fmt.Sprintf(`{"plan_id":%q}`, id), nil).Code != 409 {
		t.Fatal("expired plan accepted")
	}
	_ = f.request("/api/scan", "{}", nil)
	id = planID(t, f)
	_ = f.request("/api/scan", "{}", nil)
	if f.request("/api/probe", fmt.Sprintf(`{"plan_id":%q}`, id), nil).Code != 409 {
		t.Fatal("new baseline did not invalidate plan")
	}
	if f.request("/api/close", "{}", nil).Code != 200 || f.closes.Load() != 1 {
		t.Fatal("close action failed")
	}
	if f.request("/api/scan", "{}", nil).Code != 410 {
		t.Fatal("closed session reused")
	}
}

func TestStaticSurfaceAndActualLoopback(t *testing.T) {
	f := setup(t)
	server := httptest.NewUnstartedServer(f.h)
	f.h.host = server.Listener.Addr().String()
	f.h.origin = "http://" + f.h.host
	server.Start()
	defer server.Close()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 || len(data) == 0 {
			t.Fatal("asset missing")
		}
		if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unsafe headers")
		}
	}
	for _, path := range []string{"/../README.md", "/health?target=x", "/api/report", "/robots.txt"} {
		r := httptest.NewRequest("GET", "http://"+f.h.host+path, nil)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code == 200 || w.Code/100 == 3 {
			t.Fatal("unexpected static or redirected route")
		}
	}
	body := bytes.NewBufferString("{}")
	r, _ := http.NewRequest("POST", server.URL+"/api/session", body)
	r.Header.Set("Origin", server.URL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Bootstrap", f.bootstrap)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || f.scans.Load() != 0 || f.probes.Load() != 0 {
		t.Fatal("loopback startup performed work")
	}
}

// Optional, isolated browser harness. The normal suite skips it. It never uses
// SystemIO/SystemNetwork; even the confirm button only returns fictional evidence.
func TestUIFixture(t *testing.T) {
	path := os.Getenv("CC_HEALTH_UI_FIXTURE")
	if path == "" {
		t.Skip("manual isolated browser harness")
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" || !strings.HasPrefix(filepath.Clean(path), filepath.Clean(tmp)+string(os.PathSeparator)) {
		t.Fatal("fixture URL file must be inside TMPDIR")
	}
	f := setup(t)
	f.now = time.Now().UTC()
	scan := f.h.actions.Scan
	f.h.actions.Scan = func(ctx context.Context) health.Snapshot {
		s := scan(ctx)
		s.Report.Scope = "隔离 UI fixture：所有环境数据均为虚构，未读取真实配置或执行外联。"
		return s
	}
	done := make(chan struct{})
	var once sync.Once
	f.h.actions.Close = func() { once.Do(func() { close(done) }) }
	server := httptest.NewUnstartedServer(f.h)
	f.h.host = server.Listener.Addr().String()
	f.h.origin = "http://" + f.h.host
	server.Start()
	defer server.Close()
	if err := os.WriteFile(path, []byte(server.URL+"/#"+f.bootstrap), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
	}
}
