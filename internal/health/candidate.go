package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"
)

func digest(data []byte) string { d := sha256.Sum256(data); return hex.EncodeToString(d[:]) }

// Fingerprint stays server-side. It binds observations without retaining settings.
func (s Snapshot) Fingerprint() string {
	states := []string{}
	for _, c := range s.Report.Checks {
		if c.ID == "configuration" || c.ID == "installation" {
			states = append(states, c.ID+":"+string(c.Status)+":"+c.Code)
		}
	}
	b, _ := json.Marshal([]any{s.input.OS, s.input.Home, s.input.Directory, s.cliPath, s.environmentProof, s.sourceProof, states})
	return digest(b)
}
func (s Snapshot) Command() (string, string) { return s.cliPath, s.input.Directory }

// CandidateReference represents the explicitly chosen shell route, not a claim
// about Claude's final settings. Unknown project/managed/remote policy stays visible.
func (s Snapshot) CandidateReference(proxy string, now time.Time) (Snapshot, string) {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return Snapshot{}, "invalid_local_proxy"
	}
	if s.input.OS != "darwin" || s.cliPath == "" || s.input.Directory == "" {
		return Snapshot{}, "launch_context_unavailable"
	}
	for key := range s.input.Env {
		if loaderKey(key) {
			return Snapshot{}, "external_loader_policy_unresolved"
		}
	}
	for _, key := range providerKeys {
		if _, ok := s.input.Env[key]; ok {
			return Snapshot{}, "provider_target_unresolved"
		}
	}
	for _, key := range []string{"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE", "NODE_EXTRA_CA_CERTS", "CLAUDE_CODE_CERT_STORE", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := s.input.Env[key]; ok && value != "" {
			return Snapshot{}, "unsupported_connection_policy"
		}
	}
	if s.input.Env["NODE_TLS_REJECT_UNAUTHORIZED"] == "0" {
		return Snapshot{}, "unsafe_tls_policy"
	}
	for _, c := range s.Report.Checks {
		if c.ID != "configuration" {
			continue
		}
		if c.Status == Fail {
			return Snapshot{}, "invalid_configuration"
		}
		for _, step := range c.Steps {
			if step.ID == "user_settings" && step.Status == Unknown {
				return Snapshot{}, "user_settings_unreadable"
			}
			if step.ID == "user_settings" && step.Status == Pass {
				if s.input.ReadSettings == nil {
					return Snapshot{}, "user_settings_unreadable"
				}
				data, e := s.input.ReadSettings()
				if e != nil || !settingsMatchesCandidate(data, proxy) {
					return Snapshot{}, "observed_settings_conflict"
				}
			}
		}
	}
	candidate := s
	candidate.Report.Checks = append([]Check(nil), s.Report.Checks...)
	candidate.route = SelectRoute(map[string]string{"HTTPS_PROXY": proxy})
	candidate.Report.Scope = "本次所选新进程代理环境的固定参考链路；Claude 实际有效路由、project/managed/remote/MDM、账户与模型未验证"
	candidate.Report.RepairAvailable = true
	candidate.Report.set("proxy", Pass, "selected_child_proxy", "参考请求使用本次明确选择的本机 HTTP 代理；不证明 Claude 最终采用它。", "approved child environment", now, nil)
	return candidate, ""
}

func settingsMatchesCandidate(data []byte, proxy string) bool {
	valid, _ := settingsShape(data)
	if !valid {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return false
	}
	if obj["forceLoginMethod"] != nil || obj["forceLoginGatewayUrl"] != nil {
		return false
	}
	var env map[string]json.RawMessage
	if obj["env"] != nil && json.Unmarshal(obj["env"], &env) != nil {
		return false
	}
	for key, raw := range env {
		if providerKey(key) || loaderKey(key) {
			return false
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
		switch key {
		case "http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY":
			if value != "" && value != proxy {
				return false
			}
		case "NO_PROXY", "no_proxy":
			match, known := matchBypass(value, TargetHost, "443")
			if match || !known {
				return false
			}
		default:
			for _, allowed := range environmentKeys {
				if key == allowed && strings.TrimSpace(value) != "" {
					return false
				}
			}
		}
	}
	return true
}
