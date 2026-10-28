package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const TargetHost = "api.anthropic.com"

// Presence of a provider/gateway selector prevents assuming the public default.
// Snapshots keep presence rather than values (including custom headers).
// Environment enumeration still brings raw NAME=value data into local memory. This policy
// is not a claim that every present variable is active in every Claude version.
var providerKeys = []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_AWS_BASE_URL", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "ANTHROPIC_FOUNDRY_RESOURCE", "ANTHROPIC_CUSTOM_HEADERS"}

// A prefix policy includes future dyld controls; snapshots retain names only.
func loaderKey(key string) bool { return strings.HasPrefix(key, "DYLD_") }

var environmentKeys = append([]string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY", "ALL_PROXY", "all_proxy", "CLAUDE_CODE_PROXY_RESOLVES_HOSTS", "NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED", "CLAUDE_CODE_CERT_STORE", "CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE", "SSL_CERT_FILE", "SSL_CERT_DIR"}, providerKeys...)

func providerKey(key string) bool {
	for _, p := range providerKeys {
		if key == p {
			return true
		}
	}
	return false
}

// LocalIO is injected in tests. No default adapter is invoked by fixtures.
type LocalIO struct {
	OS             string
	Home           string
	Directory      string
	Env            map[string]string
	Lookup         func(string) (string, error)
	Version        func(context.Context, string) (string, error)
	Stat           func(string) (fs.FileInfo, error)
	ReadSettings   func() ([]byte, error)
	ResolverConfig func() ([]byte, error)
	Now            func() time.Time
}

func SystemIO() LocalIO {
	env := environmentSnapshot(os.LookupEnv, os.Environ()...)
	home, _ := os.UserHomeDir()
	dir, _ := os.Getwd()
	return LocalIO{OS: runtime.GOOS, Home: home, Directory: dir, Env: env, Lookup: exec.LookPath, Version: sandboxVersion, Stat: os.Lstat,
		ReadSettings: func() ([]byte, error) { return readSettings(home) },
		ResolverConfig: func() ([]byte, error) {
			f, err := os.Open("/etc/resolv.conf")
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return io.ReadAll(io.LimitReader(f, 32768))
		}, Now: time.Now}
}

func environmentSnapshot(lookup func(string) (string, bool), entries ...string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		// Environ entries already contain values in local memory; this branch
		// uses only the name and retains no loader value in the snapshot.
		key, _, _ := strings.Cut(entry, "=")
		if loaderKey(key) {
			env[key] = "present"
		}
	}
	for _, k := range environmentKeys {
		// LookupEnv retrieves the value too; discard it and keep presence only.
		if strings.HasPrefix(k, "CLAUDE_CODE_CLIENT_") || providerKey(k) || loaderKey(k) {
			if _, ok := lookup(k); ok {
				env[k] = "present"
			}
			continue
		}
		if value, ok := lookup(k); ok {
			env[k] = value
		}
	}
	return env
}

func readSettings(home string) ([]byte, error) {
	if home == "" {
		return nil, errors.New("home_unavailable")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(".claude/settings.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("settings_not_regular")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, err
	}
	if len(data) > 65536 {
		return nil, errors.New("settings_too_large")
	}
	return data, nil
}

// Inspect syntax and env shape, then retain only known network-key presence.
// Raw bytes/decoded strings can contain secrets. This function returns only
// structure/presence results; the caller retains a whole-file binding digest.
// No raw decoder errors are exposed; memory is not explicitly zeroed.
func settingsShape(data []byte) (valid bool, networkOverride bool) {
	if len(data) > 65536 || !json.Valid(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return false, false
	}
	var object struct {
		Env                  json.RawMessage `json:"env"`
		ForceLoginMethod     json.RawMessage `json:"forceLoginMethod"`
		ForceLoginGatewayURL json.RawMessage `json:"forceLoginGatewayUrl"`
	}
	if json.Unmarshal(data, &object) != nil {
		return false, false
	}
	networkOverride = object.ForceLoginMethod != nil || object.ForceLoginGatewayURL != nil
	if object.Env == nil {
		return true, networkOverride
	}
	if len(bytes.TrimSpace(object.Env)) == 0 || bytes.TrimSpace(object.Env)[0] != '{' {
		return false, false
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(object.Env, &env) != nil {
		return false, false
	}
	for key, value := range env {
		if loaderKey(key) {
			networkOverride = true
			if len(value) == 0 || value[0] != '"' {
				return false, false
			}
			continue // JSON syntax already validated; do not decode a loader value.
		}
		var text string
		if json.Unmarshal(value, &text) != nil || string(value) == "null" {
			return false, false
		}
		for _, allowed := range environmentKeys {
			if key == allowed {
				networkOverride = true
			}
		}
	}
	return true, networkOverride
}

type cappedWriter struct {
	bytes.Buffer
	limit int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.Len()+n > w.limit {
		return 0, errors.New("output_limit")
	}
	_, err := w.Buffer.Write(p)
	return n, err
}

// --version uses a default-deny sandbox. Only the resolved executable and fixed
// OS runtime libraries/interpreters are readable; no user directory is allowed.
// If sandbox-exec is unavailable or denied, return unknown; never fall back to an
// unsandboxed invocation. Environment credentials are not inherited by the child.
func sandboxVersion(ctx context.Context, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("executable_unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("executable_unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("executable_unavailable")
	}
	const profile = `(version 1)
(deny default)
(import "dyld-support.sb")
(allow syscall*)
(allow mach-bootstrap)
(allow signal (target self))
(allow process-exec (literal (param "CLI_BINARY")) (literal "/bin/bash") (literal "/bin/sh"))
(allow file-read* file-map-executable
  (literal (param "CLI_BINARY"))
  (literal "/bin/bash") (literal "/bin/sh")
  (subpath "/System/Library") (subpath "/usr/lib"))
(allow file-read-data (literal "/dev/null") (literal "/dev/urandom") (literal "/dev/random"))
(allow sysctl-read)
`
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-D", "CLI_BINARY="+resolved, "-p", profile, resolved, "--version")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/var/empty", "LANG=C"}
	cmd.Dir = "/var/empty"
	out := &cappedWriter{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

type Route struct {
	Proxy   *url.URL
	Source  string
	Code    string
	State   Status
	Detail  string
	Blocked bool
}

// SelectRoute intentionally does not use http.ProxyFromEnvironment: Claude's
// documented lower-case-first preference and HTTPS->HTTP fallback differ from Go.
func SelectRoute(env map[string]string) Route {
	r := Route{State: Pass, Source: "current-process allowlist", Code: "direct", Detail: "未设置受支持的代理变量；本工具参考请求采用直连。"}
	for _, key := range providerKeys {
		if _, present := env[key]; present {
			return Route{State: Unknown, Source: "current-process allowlist", Code: "provider_unresolved", Detail: "存在 provider、宿主管理或网关相关字段；是否生效与适用目标未知，已禁用默认目标探测。", Blocked: true}
		}
	}
	for _, key := range []string{"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE"} {
		if _, ok := env[key]; ok {
			return Route{State: Unknown, Source: "current-process presence", Code: "mtls_unsupported", Detail: "存在 mTLS 配置；未打开证书或私钥文件，首版不支持此连接方式。", Blocked: true}
		}
	}
	for _, key := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY"} {
		if env[key] == "" {
			continue
		}
		r.Source = key
		u, err := url.Parse(env[key])
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || !safeHost(u.Hostname()) {
			r.State = Unknown
			r.Code = "proxy_unsupported"
			r.Detail = "所选代理不是可明确解析的 HTTP(S) 代理；未回退到低优先级变量。"
			r.Blocked = true
			return r
		}
		if u.User != nil {
			r.State = Unknown
			r.Code = "proxy_auth_unsupported"
			r.Detail = "所选代理含认证信息；已在本地识别并拒绝使用，不发送该认证信息，也不写入报告。"
			r.Blocked = true
			return r
		}
		if p := u.Port(); p != "" {
			port, err := strconv.Atoi(p)
			if err != nil || port < 1 || port > 65535 {
				r.State = Fail
				r.Code = "proxy_port_invalid"
				r.Detail = "代理端口无效。"
				r.Blocked = true
				return r
			}
		}
		r.Proxy = u
		r.Code = "http_connect"
		r.Detail = "按已记录的变量优先级选择 HTTP CONNECT 代理；只代表本工具启动环境。"
		break
	}
	if r.Proxy == nil && (env["ALL_PROXY"] != "" || env["all_proxy"] != "") {
		r.State = Unknown
		r.Code = "all_proxy_unsupported"
		r.Detail = "仅发现 ALL_PROXY；官方支持与本工具链路不确定，未假定直连。"
		r.Blocked = true
		return r
	}
	if env["no_proxy"] != "" && env["NO_PROXY"] != "" && env["no_proxy"] != env["NO_PROXY"] {
		r.State = Unknown
		r.Code = "no_proxy_conflict"
		r.Detail = "大小写 NO_PROXY 值冲突，优先级未证实。"
		r.Blocked = true
		return r
	}
	bypass := env["NO_PROXY"]
	if env["no_proxy"] != "" {
		bypass = env["no_proxy"]
	}
	if r.Proxy != nil {
		match, known := matchBypass(bypass, TargetHost, "443")
		if !known {
			r.State = Unknown
			r.Code = "no_proxy_unresolved"
			r.Detail = "NO_PROXY 含当前实现无法确定的规则；未猜测直连或代理。"
			r.Blocked = true
			return r
		}
		if match {
			r.Proxy = nil
			r.Code = "no_proxy_bypass"
			r.Detail = "NO_PROXY 匹配固定目标；本工具参考请求采用直连。"
		}
	}
	if r.Proxy != nil && env["CLAUDE_CODE_PROXY_RESOLVES_HOSTS"] != "" {
		r.State = Unknown
		r.Code = "proxy_dns_override"
		r.Detail = "存在 Claude DNS 策略覆盖；首版无法复现，已禁用探测。"
		r.Blocked = true
	}
	return r
}

func safeHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return !strings.HasPrefix(host, ".") && !strings.Contains(host, "..")
}

func matchBypass(raw, host, port string) (bool, bool) {
	unknown := false
	for _, rule := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		if rule == "*" {
			return true, true
		}
		if strings.Contains(rule, "/") {
			unknown = true
			continue
		} // IP/CIDR matching needs the actual target resolution.
		if strings.HasPrefix(rule, "*.") {
			rule = rule[1:]
		}
		if strings.Contains(rule, "*") {
			unknown = true
			continue
		}
		if strings.Contains(rule, ":") {
			h, p, err := net.SplitHostPort(rule)
			if err != nil {
				unknown = true
				continue
			}
			if p != port {
				continue
			}
			rule = h
		}
		subOnly := strings.HasPrefix(rule, ".")
		rule = strings.TrimPrefix(strings.ToLower(rule), ".")
		if !safeHost(rule) {
			unknown = true
			continue
		}
		if (!subOnly && host == rule) || strings.HasSuffix(host, "."+rule) {
			return true, true
		}
	}
	return false, !unknown
}

type Snapshot struct {
	Report           Report
	route            Route
	resolverConfig   func() ([]byte, error)
	input            LocalIO
	cliPath          string
	sourceProof      []string
	environmentProof string
}

var versionPattern = regexp.MustCompile(`^v?([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4})(?: \(Claude Code\))?$`)

// Inspect opens only the fixed bounded user settings file for syntax/env shape.
// Other sources are metadata-only. The baseline retains digests and needed
// execution/route context; Report excludes raw configuration and credentials.
func Inspect(ctx context.Context, d LocalIO) Snapshot {
	return inspect(ctx, d, nil)
}

// Reinspect rechecks local configuration without executing another --version.
// The approved entry itself is held and copied by the launch adapter.
func Reinspect(ctx context.Context, d LocalIO, previous Snapshot) Snapshot {
	return inspect(ctx, d, &previous)
}

func inspect(ctx context.Context, d LocalIO, previous *Snapshot) Snapshot {
	now := d.Now().UTC()
	r := newReport(now)
	s := Snapshot{Report: r, resolverConfig: d.ResolverConfig, input: d}
	encodedEnv, _ := json.Marshal(d.Env)
	s.environmentProof = digest(encodedEnv)
	s.input.Env = map[string]string{}
	for key, value := range d.Env {
		if key == "NODE_TLS_REJECT_UNAUTHORIZED" {
			s.input.Env[key] = value
		} else if value != "" {
			s.input.Env[key] = "present"
		} else {
			s.input.Env[key] = ""
		}
	}
	if d.OS != "darwin" {
		s.Report.set("installation", Unknown, "platform_unsupported", "首版适配器仅支持 macOS。", "platform", now, nil)
		s.route = Route{State: Unknown, Blocked: true, Code: "platform_unsupported"}
		return s
	}
	path := ""
	var err error
	if previous != nil {
		path = previous.cliPath
	} else {
		path, err = d.Lookup("claude")
	}
	if err == nil {
		s.cliPath = path
	}
	if previous != nil {
		for _, check := range previous.Report.Checks {
			if check.ID == "installation" {
				for i := range s.Report.Checks {
					if s.Report.Checks[i].ID == "installation" {
						s.Report.Checks[i] = check
					}
				}
			}
		}
	} else if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		s.Report.set("installation", Fail, "cli_missing", "PATH 中未找到 Claude Code。", "PATH lookup", now, nil)
	} else if err != nil {
		s.Report.set("installation", Unknown, "lookup_unavailable", "无法判断 Claude Code 安装位置。", "PATH lookup", now, nil)
	} else {
		vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		output, verr := d.Version(vctx, path)
		cancel()
		version := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
		if verr != nil || version == nil {
			s.Report.set("installation", Unknown, "version_unavailable", "已找到可执行项；只读沙箱内版本检查不可用或输出不符合已知格式。", "sandboxed --version", now, nil)
		} else {
			s.Report.set("installation", Pass, "version_detected", "检测到 Claude Code "+version[1]+"；未执行登录或模型命令。", "sandboxed --version", now, nil)
		}
	}
	s.route = SelectRoute(d.Env)
	s.Report.set("proxy", s.route.State, s.route.Code, s.route.Detail, s.route.Source, now, nil)
	steps := []Evidence{}
	configState, configCode, configDetail := Pass, "basic_configuration_valid", "已检查固定用户配置的 JSON/env 结构；有效多层配置仍未验证。"
	for _, item := range []struct{ label, path string }{
		{"user_settings", filepath.Join(d.Home, ".claude", "settings.json")},
		{"project_settings", filepath.Join(d.Directory, ".claude", "settings.json")},
		{"project_local_settings", filepath.Join(d.Directory, ".claude", "settings.local.json")},
		{"managed_settings", "/Library/Application Support/ClaudeCode/managed-settings.json"},
		{"managed_dropins", "/Library/Application Support/ClaudeCode/managed-settings.d"},
	} {
		state, code, detail := Unknown, "present_unread", "存在配置来源；没有读取其内容。"
		if (item.label == "user_settings" && d.Home == "") || (strings.HasPrefix(item.label, "project_") && d.Directory == "") {
			code = "location_unknown"
			detail = "无法确定配置位置。"
		} else {
			info, err := d.Stat(item.path)
			if err == nil {
				s.sourceProof = append(s.sourceProof, item.label+":"+strconv.FormatInt(info.ModTime().UnixNano(), 10)+":"+strconv.FormatInt(info.Size(), 10)+":"+info.Mode().String())
			}
			if errors.Is(err, fs.ErrNotExist) {
				state = NotApplicable
				code = "absent"
				detail = "该常见本地来源不存在。"
			} else if err != nil {
				code = "metadata_unavailable"
				detail = "无法读取该来源的元数据。"
			} else if info.Mode()&os.ModeSymlink != 0 {
				code = "symlink_unread"
				detail = "配置为符号链接；没有跟随读取。"
			} else if item.label == "user_settings" && info.Mode().IsRegular() && d.ReadSettings != nil {
				data, readErr := d.ReadSettings()
				if readErr == nil {
					s.sourceProof = append(s.sourceProof, digest(data))
				}
				if readErr != nil {
					code = "settings_unreadable"
					detail = "固定用户配置无法在大小/权限/路径限制内读取。"
				} else {
					valid, override := settingsShape(data)
					if !valid {
						state = Fail
						code = "settings_invalid"
						detail = "固定用户配置不是有效 JSON 对象或 env 不是字符串映射。"
						configState = Fail
						configCode = "settings_invalid"
						configDetail = detail
					} else {
						state = Pass
						code = "syntax_valid"
						detail = "固定用户配置的原文已在本地内存校验并计算绑定摘要；报告不含原文或字段值。"
					}
					if override {
						s.route.State = Unknown
						s.route.Blocked = true
						s.Report.set("proxy", Unknown, "settings_network_override", "用户配置存在网络字段覆盖；当前 shell 不能证明 Claude 的有效路由，未执行其值。", "settings allowlist presence", now, nil)
					}
				}
			}
		}
		if state == Unknown && configState != Fail {
			configState = Unknown
			configCode = "configuration_partly_unobserved"
			configDetail = "部分本地配置来源不可读、未读取或不能确认；有效配置仍未知。"
		}
		steps = append(steps, Evidence{item.label, state, code, detail, "filesystem metadata", now})
	}
	s.Report.set("configuration", configState, configCode, configDetail, "bounded user JSON + filesystem metadata", now, steps)
	// An unread or invalid local source can hide a provider or network override.
	// Do not treat the absence of observed override fields as a default route.
	if configState != Pass {
		s.route.Blocked = true
		if s.route.State == Pass {
			s.Report.set("proxy", Unknown, "configuration_route_unresolved", "部分配置无效或未能检查；不能据此确定默认目标与路由，已禁用参考探测。", "local configuration gate", now, nil)
		}
		s.route.State = Unknown
	}
	s.Report.set("effective_configuration", Unknown, "effective_settings_unobserved", "项目、命令行、managed、remote/MDM 及已运行 Claude 的最终配置未验证；不纳入通过权重。", "scope", now, nil)
	certState, certCode, certDetail := Pass, "system_trust_reference", "本工具参考链路使用固定公开 /etc/ssl/cert.pem 强制验证 CA 与主机名；不等同 Claude 运行时证书库。"
	for _, key := range []string{"NODE_EXTRA_CA_CERTS", "CLAUDE_CODE_CERT_STORE", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if d.Env[key] != "" {
			certState = Unknown
			certCode = "custom_trust_unread"
			certDetail = "存在自定义证书策略；未读取证书文件，本工具不冒充该信任链。"
			s.route.Blocked = true
		}
	}
	if d.Env["NODE_TLS_REJECT_UNAUTHORIZED"] == "0" {
		certState = Fail
		certCode = "tls_verification_disabled"
		certDetail = "启动环境声明关闭 Node TLS 验证；本工具仍强制验证，不采用不安全设置。"
		s.route.Blocked = true
	}
	s.Report.set("certificates", certState, certCode, certDetail, "current-process allowlist", now, nil)
	s.Report.set("connection", Unknown, "not_authorized", "主动外联默认关闭；需先审阅并确认具体清单。", "no request", now, nil)
	// Resolver configuration is needed only when a user requests a plan, never during a local scan.
	return s
}
