package health

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderAndGatewayEvidenceBlocksDefaultTarget(t *testing.T) {
	// Deliberately independent of providerKeys: a missing adapter entry must fail.
	keys := []string{"CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "ANTHROPIC_BASE_URL", "ANTHROPIC_AWS_BASE_URL", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE", "ANTHROPIC_CUSTOM_HEADERS"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			for _, value := range []string{"", "0", "1", "https://FAKE_GATEWAY_PRIVATE.invalid/path"} {
				d := fixtureIO(t)
				lookedUp := map[string]bool{}
				d.Env = environmentSnapshot(func(k string) (string, bool) {
					lookedUp[k] = true
					return value, k == key
				})
				if d.Env[key] != "present" || lookedUp["ANTHROPIC_API_KEY"] || lookedUp["CLAUDE_CODE_OAUTH_TOKEN"] {
					t.Fatal("provider presence lost or credential lookup attempted")
				}
				assertNoDefaultPlan(t, d, "provider_unresolved", value)
				// A settings field has the same conservative meaning, including "0";
				// presence is evidence of ambiguity, not proof the flag is enabled.
				d = fixtureIO(t)
				data, _ := json.Marshal(map[string]any{"env": map[string]string{key: value}})
				if err := os.WriteFile(filepath.Join(d.Home, ".claude", "settings.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				assertNoDefaultPlan(t, d, "settings_network_override", value)
			}
		})
	}
	for _, key := range []string{"forceLoginMethod", "forceLoginGatewayUrl"} {
		t.Run(key, func(t *testing.T) {
			for _, value := range []any{"FAKE_GATEWAY_PRIVATE", nil, false} {
				d := fixtureIO(t)
				data, _ := json.Marshal(map[string]any{key: value})
				if err := os.WriteFile(filepath.Join(d.Home, ".claude", "settings.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				assertNoDefaultPlan(t, d, "settings_network_override", "FAKE_GATEWAY_PRIVATE")
			}
		})
	}
}

func assertNoDefaultPlan(t *testing.T, d LocalIO, code, secret string) {
	t.Helper()
	d.ResolverConfig = func() ([]byte, error) { t.Fatal("blocked plan read resolver"); return nil, nil }
	s := Inspect(context.Background(), d)
	p := s.Plan(testTime)
	if !s.route.Blocked || p.Allowed || find(s.Report, "proxy").Status != Unknown || find(s.Report, "proxy").Code != code {
		t.Fatalf("ambiguous route allowed: proxy=%s plan=%v", find(s.Report, "proxy").Code, p.Allowed)
	}
	n := NetworkIO{Roots: x509.NewCertPool(), Now: func() time.Time { return testTime }, Resolve: func(context.Context, string, string) ([]net.IP, error) {
		t.Fatal("blocked plan resolved")
		return nil, nil
	}, Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("blocked plan dialed")
		return nil, nil
	}}
	r := s.Probe(context.Background(), p, n)
	if find(r, "connection").Code != "plan_invalid" {
		t.Fatal("blocked plan executable")
	}
	raw, _ := json.Marshal([]any{s.Report, p, r})
	if (d.Home != "" && strings.Contains(string(raw), d.Home)) || (len(secret) > 3 && strings.Contains(string(raw), secret)) {
		t.Fatal("private configuration leaked")
	}
}

func TestUnobservedLocalConfigurationBlocksPlan(t *testing.T) {
	for _, name := range []string{"user permission", "user invalid", "user symlink", "user location unknown", "project settings", "project local settings", "managed settings", "managed dropins", "metadata denied"} {
		t.Run(name, func(t *testing.T) {
			d := fixtureIO(t)
			path := filepath.Join(d.Home, ".claude", "settings.json")
			switch name {
			case "user permission":
				if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				d.ReadSettings = func() ([]byte, error) { return nil, fs.ErrPermission }
			case "user invalid":
				if err := os.WriteFile(path, []byte(`{"env":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "user symlink":
				if err := os.Symlink(filepath.Join(d.Home, "fake-secret"), path); err != nil {
					t.Fatal(err)
				}
			case "user location unknown":
				d.Home = ""
			default:
				// Use a fake file's metadata even for managed paths: never stat the host.
				if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				target := map[string]string{"project settings": filepath.Join(d.Directory, ".claude", "settings.json"), "project local settings": filepath.Join(d.Directory, ".claude", "settings.local.json"), "managed settings": "/Library/Application Support/ClaudeCode/managed-settings.json", "managed dropins": "/Library/Application Support/ClaudeCode/managed-settings.d"}[name]
				d.Stat = func(p string) (fs.FileInfo, error) {
					if name == "metadata denied" {
						return nil, fs.ErrPermission
					}
					if p == target {
						return info, nil
					}
					return nil, fs.ErrNotExist
				}
			}
			assertNoDefaultPlan(t, d, "configuration_route_unresolved", "")
		})
	}
}

func TestSpecialDNSAnswersNeverDial(t *testing.T) {
	blocked := []string{"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.64.0.1", "100.127.255.254", "127.0.0.1", "127.99.1.1", "169.254.169.254", "172.16.0.1", "172.31.255.254", "192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.168.1.1", "192.175.48.1", "198.18.0.1", "198.19.255.254", "198.51.100.1", "203.0.113.1", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1", "ff02::1", "2001:db8::1", "2606:4700:4700::1111", "::ffff:127.0.0.1", "invalid"}
	for _, value := range blocked {
		t.Run(value, func(t *testing.T) {
			if publicIPv4(value) {
				t.Fatal("special address accepted")
			}
			for _, proxy := range []bool{false, true} {
				for _, order := range []int{0, 1, 2} {
					d := fixtureIO(t)
					if proxy {
						d.Env["HTTPS_PROXY"] = "http://proxy.test:8080"
					}
					s := Inspect(context.Background(), d)
					p := s.Plan(testTime)
					ips := []net.IP{net.ParseIP(value)}
					if order == 1 {
						ips = append([]net.IP{net.ParseIP("93.184.215.14")}, ips...)
					}
					if order == 2 {
						ips = append(ips, net.ParseIP("93.184.215.14"))
					}
					resolved, dialed := 0, 0
					n := NetworkIO{Roots: x509.NewCertPool(), Now: func() time.Time { return testTime }, Resolve: func(context.Context, string, string) ([]net.IP, error) { resolved++; return ips, nil }, Dial: func(context.Context, string, string) (net.Conn, error) {
						dialed++
						return nil, errors.New("unexpected dial")
					}}
					r := s.Probe(context.Background(), p, n)
					if resolved != 1 || dialed != 0 || find(r, "connection").Code != "resolved_address_disallowed" {
						t.Fatalf("bad answer dialed: resolves=%d dials=%d code=%s", resolved, dialed, find(r, "connection").Code)
					}
				}
			}
		})
	}
	for _, value := range []string{"93.184.215.14", "1.1.1.1", "100.63.255.254", "100.128.0.1", "172.32.0.1", "198.20.0.1"} {
		if !publicIPv4(value) {
			t.Fatalf("public control rejected: %s", value)
		}
	}
}

func TestLiteralLoopbackExceptionOnlyForSelectedProxy(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "::1"} {
		t.Run(value, func(t *testing.T) {
			d := fixtureIO(t)
			d.Env["HTTPS_PROXY"] = "http://" + net.JoinHostPort(value, "4567")
			d.ResolverConfig = func() ([]byte, error) { t.Fatal("literal proxy resolver access"); return nil, nil }
			s := Inspect(context.Background(), d)
			p := s.Plan(testTime)
			if !p.Allowed {
				t.Fatal("explicit loopback proxy rejected")
			}
			dials := 0
			n := NetworkIO{Roots: x509.NewCertPool(), Now: func() time.Time { return testTime }, Resolve: func(context.Context, string, string) ([]net.IP, error) { t.Fatal("literal proxy DNS"); return nil, nil }, Dial: func(_ context.Context, network, address string) (net.Conn, error) {
				dials++
				if network != "tcp" || address != net.JoinHostPort(value, "4567") {
					t.Fatal("unapproved proxy dial")
				}
				return nil, errors.New("fixture stops before actual dial")
			}}
			_ = s.Probe(context.Background(), p, n)
			if dials != 1 {
				t.Fatal("selected literal proxy did not reach transport")
			}
			p.proxy = nil // Internal forged plan tests the execution boundary too.
			dials = 0
			r := s.Probe(context.Background(), p, n)
			if dials != 0 || find(r, "connection").Code != "resolved_address_disallowed" {
				t.Fatal("proxy exception extended to target")
			}
		})
	}
	for _, value := range []string{"10.1.2.3", "169.254.1.1", "100.64.1.1", "192.0.2.1", "fc00::1"} {
		d := fixtureIO(t)
		d.Env["HTTPS_PROXY"] = "http://" + net.JoinHostPort(value, "8080")
		d.ResolverConfig = func() ([]byte, error) { t.Fatal("rejected proxy resolver access"); return nil, nil }
		if Inspect(context.Background(), d).Plan(testTime).Allowed {
			t.Fatal("unapproved literal proxy")
		}
	}
}
