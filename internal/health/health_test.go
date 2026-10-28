package health

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func fixtureIO(t *testing.T) LocalIO {
	t.Helper()
	home := filepath.Join(t.TempDir(), "fixture-home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	return LocalIO{OS: "darwin", Home: home, Directory: filepath.Join(home, "fixture-project"), Env: map[string]string{}, Now: func() time.Time { return testTime },
		Lookup: func(string) (string, error) { return "/fixture/private-user/bin/claude", nil }, Version: func(context.Context, string) (string, error) { return "2.1.999 (Claude Code)", nil },
		Stat: func(path string) (fs.FileInfo, error) {
			if strings.HasPrefix(path, home+string(os.PathSeparator)) {
				return os.Lstat(path)
			}
			return nil, fs.ErrNotExist
		},
		ReadSettings: func() ([]byte, error) { return readSettings(home) }, ResolverConfig: func() ([]byte, error) { return []byte("nameserver 192.0.2.53\n"), nil }}
}

func find(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	panic("missing check")
}

func TestLocalScanHasNoProbeAndNoNumericScore(t *testing.T) {
	d := fixtureIO(t)
	dns := 0
	d.ResolverConfig = func() ([]byte, error) { dns++; return nil, errors.New("must not run") }
	s := Inspect(context.Background(), d)
	if dns != 0 || s.Report.Score.Value != nil || s.Report.Score.Coverage != .65 || find(s.Report, "connection").Status != Unknown {
		t.Fatalf("wrong offline evidence: %+v", s.Report.Score)
	}
	if find(s.Report, "installation").Status != Pass || find(s.Report, "configuration").Status != Pass {
		t.Fatal("local fixture should be inspected")
	}
	if find(s.Report, "account").Status != Unknown || find(s.Report, "effective_configuration").Status != Unknown {
		t.Fatal("unobserved scope was promoted")
	}
}

func TestScoreDenominatorUnknownAndCaps(t *testing.T) {
	base := []Check{}
	for _, p := range policy {
		base = append(base, Check{Evidence: Evidence{ID: p.id, Status: Pass}})
	}
	cases := []struct {
		name, id string
		state    Status
		score    int
		coverage float64
	}{
		{"all", "", Pass, 100, 1}, {"config unknown", "configuration", Unknown, 90, .9}, {"low coverage", "proxy", Unknown, -1, .75},
		{"critical unknown", "installation", Unknown, -1, .8}, {"connection failed", "connection", Fail, 49, 1},
		{"installation failed", "installation", Fail, 49, 1}, {"invalid config caps startup", "configuration", Fail, 49, 1},
		{"NA cannot shrink denominator", "connection", NotApplicable, -1, .65}, {"bad state", "proxy", Status("bogus"), -1, .75},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			checks := append([]Check(nil), base...)
			for i := range checks {
				if checks[i].ID == tt.id {
					checks[i].Status = tt.state
				}
			}
			s := Evaluate(checks)
			if s.Coverage != tt.coverage {
				t.Fatalf("coverage %v", s.Coverage)
			}
			if tt.score < 0 {
				if s.Value != nil {
					t.Fatal("unknown should hide score")
				}
			} else if s.Value == nil || *s.Value != tt.score {
				t.Fatalf("score %+v", s)
			}
		})
	}
	duplicate := append(append([]Check(nil), base...), base[0])
	if Evaluate(duplicate).Value != nil {
		t.Fatal("duplicates must fail closed")
	}
	if s := Evaluate(nil); s.Value != nil || s.ApplicableWeight != 100 || s.Coverage != 0 {
		t.Fatal("missing checks silently reduced denominator")
	}
}

func TestProxyPolicy(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		code  string
		host  string
		state Status
	}{
		{"direct", map[string]string{}, "direct", "", Pass},
		{"lower https first", map[string]string{"https_proxy": "http://first.test:8080", "HTTPS_PROXY": "http://second.test", "http_proxy": "http://third.test"}, "http_connect", "first.test", Pass},
		{"upper https before lower http", map[string]string{"HTTPS_PROXY": "http://second.test", "http_proxy": "http://third.test"}, "http_connect", "second.test", Pass},
		{"http fallback", map[string]string{"HTTP_PROXY": "http://fallback.test"}, "http_connect", "fallback.test", Pass},
		{"space no proxy", map[string]string{"HTTPS_PROXY": "http://proxy.test", "NO_PROXY": "other.test api.anthropic.com"}, "no_proxy_bypass", "", Pass},
		{"domain no proxy", map[string]string{"HTTPS_PROXY": "http://proxy.test", "NO_PROXY": ".anthropic.com:443"}, "no_proxy_bypass", "", Pass},
		{"wildcard", map[string]string{"HTTPS_PROXY": "http://proxy.test", "NO_PROXY": "*"}, "no_proxy_bypass", "", Pass},
		{"suffix boundary", map[string]string{"HTTPS_PROXY": "http://proxy.test", "NO_PROXY": "thropic.com"}, "http_connect", "proxy.test", Pass},
		{"no proxy conflict", map[string]string{"HTTPS_PROXY": "http://proxy.test", "no_proxy": "a", "NO_PROXY": "b"}, "no_proxy_conflict", "proxy.test", Unknown},
		{"cidr unresolved", map[string]string{"HTTPS_PROXY": "http://proxy.test", "NO_PROXY": "10.0.0.0/8"}, "no_proxy_unresolved", "proxy.test", Unknown},
		{"socks", map[string]string{"https_proxy": "socks5://proxy.test", "HTTPS_PROXY": "http://fallback.test"}, "proxy_unsupported", "", Unknown},
		{"auth", map[string]string{"HTTPS_PROXY": "http://fake-user:fake-secret@proxy.test"}, "proxy_auth_unsupported", "", Unknown},
		{"invalid url", map[string]string{"HTTPS_PROXY": "http://x%wrong"}, "proxy_unsupported", "", Unknown},
		{"ALL_PROXY", map[string]string{"ALL_PROXY": "http://proxy.test"}, "all_proxy_unsupported", "", Unknown},
		{"custom provider", map[string]string{"ANTHROPIC_BASE_URL": "https://custom.test/private"}, "provider_unresolved", "", Unknown},
		{"mtls", map[string]string{"CLAUDE_CODE_CLIENT_KEY": "presence-only"}, "mtls_unsupported", "", Unknown},
		{"proxy DNS override", map[string]string{"HTTPS_PROXY": "http://proxy.test", "CLAUDE_CODE_PROXY_RESOLVES_HOSTS": "0"}, "proxy_dns_override", "proxy.test", Unknown},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := SelectRoute(tt.env)
			host := ""
			if r.Proxy != nil {
				host = r.Proxy.Hostname()
			}
			if r.Code != tt.code || r.State != tt.state || host != tt.host {
				t.Fatalf("got %s/%s/%s", r.Code, r.State, host)
			}
			if r.State == Unknown && !r.Blocked {
				t.Fatal("ambiguous route must block probe")
			}
		})
	}
}

func TestInstallationUnknownVersusMissing(t *testing.T) {
	for _, tt := range []struct {
		name    string
		lookup  error
		version string
		verr    error
		state   Status
	}{
		{"missing", exec.ErrNotFound, "", nil, Fail}, {"permission", fs.ErrPermission, "", nil, Unknown},
		{"version timeout", nil, "", context.DeadlineExceeded, Unknown}, {"sandbox unavailable", nil, "", fs.ErrPermission, Unknown},
		{"untrusted version output", nil, "secret-token /Users/private 2.1.999", nil, Unknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := fixtureIO(t)
			d.Lookup = func(string) (string, error) { return "/fixture", tt.lookup }
			d.Version = func(context.Context, string) (string, error) { return tt.version, tt.verr }
			s := Inspect(context.Background(), d)
			if find(s.Report, "installation").Status != tt.state {
				t.Fatal("unexpected status")
			}
			data, _ := json.Marshal(s.Report)
			if strings.Contains(string(data), "secret-token") || strings.Contains(string(data), "/Users/private") {
				t.Fatal("version output leaked")
			}
		})
	}
}

func TestSettingsBoundedShapeAndPrivacy(t *testing.T) {
	for _, tt := range []struct {
		name, data string
		state      Status
		blocked    bool
	}{
		{"valid", `{"env":{"UNRELATED_TOKEN":"SECRET_FIXTURE_7","OTHER":"text"},"apiKeyHelper":"never execute"}`, Pass, false},
		{"invalid", `{"secret":"SECRET_FIXTURE_7",`, Fail, true},
		{"wrong env shape", `{"env":["SECRET_FIXTURE_7"]}`, Fail, true},
		{"wrong value shape", `{"env":{"OTHER":12}}`, Fail, true},
		{"null value", `{"env":{"OTHER":null}}`, Fail, true},
		{"network override", `{"env":{"HTTPS_PROXY":"http://fake-user:SECRET_FIXTURE_7@198.51.100.8:88"}}`, Pass, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := fixtureIO(t)
			if err := os.WriteFile(filepath.Join(d.Home, ".claude", "settings.json"), []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			s := Inspect(context.Background(), d)
			if find(s.Report, "configuration").Status != tt.state || s.route.Blocked != tt.blocked {
				t.Fatalf("unexpected checks: %s blocked=%v", find(s.Report, "configuration").Status, s.route.Blocked)
			}
			data, _ := json.Marshal(s.Report)
			for _, secret := range []string{"SECRET_FIXTURE_7", "fake-user", "198.51.100.8", d.Home, "never execute"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("private input leaked")
				}
			}
		})
	}
	for _, kind := range []string{"permission", "oversize", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			d := fixtureIO(t)
			path := filepath.Join(d.Home, ".claude", "settings.json")
			switch kind {
			case "permission":
				_ = os.WriteFile(path, []byte(`{}`), 0600)
				d.ReadSettings = func() ([]byte, error) { return nil, fs.ErrPermission }
			case "oversize":
				_ = os.WriteFile(path, []byte(strings.Repeat(" ", 65537)), 0600)
			case "symlink":
				_ = os.Symlink("/not-read/secret", path)
			case "directory":
				_ = os.Mkdir(path, 0700)
			}
			s := Inspect(context.Background(), d)
			if find(s.Report, "configuration").Status != Unknown {
				t.Fatal("unreadable config became known")
			}
		})
	}
	t.Run("root escape rejected", func(t *testing.T) {
		d := fixtureIO(t)
		_ = os.Remove(filepath.Join(d.Home, ".claude"))
		outside := t.TempDir()
		_ = os.WriteFile(filepath.Join(outside, "settings.json"), []byte(`{}`), 0600)
		_ = os.Symlink(outside, filepath.Join(d.Home, ".claude"))
		if _, err := readSettings(d.Home); err == nil {
			t.Fatal("escaped fixed root")
		}
	})
}

func TestSensitiveInputIsBoundWithoutEnteringReports(t *testing.T) {
	d := fixtureIO(t)
	settings := filepath.Join(d.Home, ".claude", "settings.json")
	raw := `{"apiKey":"FAKE_SETTINGS_KEY_6","env":{"UNRELATED_TOKEN":"FAKE_CONFIG_VALUE_6"}}`
	if e := os.WriteFile(settings, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	values := map[string]string{"ANTHROPIC_API_KEY": "FAKE_ENV_API_KEY_6", "HTTPS_PROXY": "http://FAKE_PROXY_USER_6:FAKE_PROXY_PASSWORD_6@proxy.test:8080", "DYLD_FUTURE_CONTROL": "FAKE_DYLD_VALUE_6"}
	entries := []string{}
	for key, value := range values {
		entries = append(entries, key+"="+value)
	}
	d.Env = environmentSnapshot(func(key string) (string, bool) { value, ok := values[key]; return value, ok }, entries...)
	originalInfo, e := os.Stat(settings)
	if e != nil {
		t.Fatal(e)
	}
	before := Inspect(context.Background(), d)
	plan := before.Plan(testTime)
	if plan.Allowed {
		t.Fatal("authenticated proxy unexpectedly allowed")
	}
	report, _ := json.Marshal(before.Report)
	manifest, _ := json.Marshal(plan)
	for _, secret := range []string{"FAKE_SETTINGS_KEY_6", "FAKE_CONFIG_VALUE_6", "FAKE_ENV_API_KEY_6", "FAKE_PROXY_USER_6", "FAKE_PROXY_PASSWORD_6", "FAKE_DYLD_VALUE_6", d.Home, d.Directory} {
		if strings.Contains(string(report)+string(manifest), secret) {
			t.Fatal("sensitive input entered report/plan")
		}
	}
	// Full settings content participates in consent binding even when this field
	// is not a network setting; disclose that digest rather than claiming presence-only.
	if e := os.WriteFile(settings, []byte(strings.Replace(raw, "FAKE_CONFIG_VALUE_6", "FAKE_CONFIG_OTHER_6", 1)), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Chtimes(settings, originalInfo.ModTime(), originalInfo.ModTime()); e != nil {
		t.Fatal(e)
	}
	afterSettings := Inspect(context.Background(), d)
	if before.Fingerprint() == afterSettings.Fingerprint() {
		t.Fatal("settings content absent from binding")
	}
	d.Env["HTTPS_PROXY"] = "http://FAKE_PROXY_USER_6:FAKE_PROXY_CHANGED_6@proxy.test:8080"
	afterEnv := Inspect(context.Background(), d)
	if afterSettings.Fingerprint() == afterEnv.Fingerprint() {
		t.Fatal("allowlisted environment absent from binding")
	}
	if strings.Contains(string(report), before.Fingerprint()) || strings.Contains(string(report), before.environmentProof) {
		t.Fatal("private binding digest exported")
	}
}

func TestPlansAreExplicitAndBounded(t *testing.T) {
	d := fixtureIO(t)
	s := Inspect(context.Background(), d)
	p := s.Plan(testTime)
	if !p.Allowed || p.TimeoutSeconds != 12 || p.Target != "https://api.anthropic.com/" || p.ExpiresAt.Sub(testTime) != time.Minute {
		t.Fatal("bad manifest")
	}
	if p := s.Plan(testTime.Add(3 * time.Minute)); p.Allowed {
		t.Fatal("stale local evidence allowed")
	}
	d.Env["HTTPS_PROXY"] = "http://user:PRIVATE_SECRET@proxy.test"
	s = Inspect(context.Background(), d)
	p = s.Plan(testTime)
	raw, _ := json.Marshal(p)
	if p.Allowed || strings.Contains(string(raw), "PRIVATE_SECRET") || strings.Contains(string(raw), "user:") {
		t.Fatal("unsafe manifest")
	}
	d = fixtureIO(t)
	d.ResolverConfig = func() ([]byte, error) { return nil, fs.ErrPermission }
	s = Inspect(context.Background(), d)
	if s.Plan(testTime).Allowed {
		t.Fatal("unknown DNS recipient should block")
	}
	d = fixtureIO(t)
	d.Env["HTTPS_PROXY"] = "http://127.0.0.1:1234"
	d.ResolverConfig = func() ([]byte, error) { t.Fatal("literal proxy must not read resolver config"); return nil, nil }
	s = Inspect(context.Background(), d)
	if !s.Plan(testTime).Allowed {
		t.Fatal("literal proxy requires no local DNS")
	}
}

func TestRealSandboxWithFakeCLI(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS sandbox adapter")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	script := filepath.Join(root, "fake-claude")
	port := listener.Addr().(*net.TCPAddr).Port
	secretFiles := []string{filepath.Join(root, "fake-home", ".claude", ".credentials.json"), filepath.Join(root, "fake-home", "Library", "Application Support", "FakeBrowser", "Cookies"), filepath.Join(root, "adjacent-secret")}
	readAttempts := ""
	for _, path := range secretFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("FAKE_SENSITIVE_CONTENT\n"), 0600); err != nil {
			t.Fatal(err)
		}
		readAttempts += "if IFS= read -r value < '" + path + "'; then printf 'UNAUTHORIZED_READ'; exit 93; fi\n"
	}
	alias := filepath.Join(root, "secret-alias")
	if err := os.Symlink(secretFiles[0], alias); err != nil {
		t.Fatal(err)
	}
	readAttempts += "if IFS= read -r value < '" + alias + "'; then printf 'UNAUTHORIZED_READ'; exit 93; fi\n"
	// Bash performs both redirections itself; no child nc/curl or real CLI is used.
	source := "#!/bin/bash\n" + readAttempts + "if printf blocked > '" + marker + "'; then exit 91; fi\nif : > /dev/tcp/127.0.0.1/" + strconv.Itoa(port) + "; then exit 92; fi\nprintf '2.1.999 (Claude Code)\\n'\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := sandboxVersion(ctx, script)
	if err != nil {
		t.Fatalf("fake CLI sandbox unavailable: %T", err)
	}
	if strings.TrimSpace(out) != "2.1.999 (Claude Code)" {
		t.Fatal("fake CLI sandbox policy failed")
	}
	d := fixtureIO(t)
	d.Lookup = func(string) (string, error) { return script, nil }
	d.Version = sandboxVersion
	if find(Inspect(ctx, d).Report, "installation").Status != Pass {
		t.Fatal("normal installation detection lost under constrained sandbox")
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("sandbox allowed file write")
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("sandbox allowed loopback network")
	}
	if err := os.WriteFile(script, []byte("#!/bin/bash\nif ( printf 'CHILD_EXECUTED' ); then exit 0; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	forkCtx, forkCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer forkCancel()
	forkOut, _ := sandboxVersion(forkCtx, script)
	if strings.Contains(forkOut, "CHILD_EXECUTED") {
		t.Fatal("sandbox allowed derived process")
	}
	if err := os.WriteFile(script, []byte("#!/bin/bash\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	timeCtx, stop := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := sandboxVersion(timeCtx, script); err == nil || time.Since(start) > time.Second {
		t.Fatal("fake process timeout did not stop")
	}
}
