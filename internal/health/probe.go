package health

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Plan is the complete direct-recipient manifest. It contains no credentials or
// machine paths. DNS/proxy addresses are displayed only for deliberate consent;
// they do not enter Report, server logs, or exports.
type Plan struct {
	ID             string    `json:"id"`
	Allowed        bool      `json:"allowed"`
	Reason         string    `json:"reason"`
	ExpiresAt      time.Time `json:"expires_at"`
	Target         string    `json:"target"`
	Route          string    `json:"route"`
	Recipients     []string  `json:"recipients"`
	Operations     []string  `json:"operations"`
	Data           []string  `json:"data"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	Notes          []string  `json:"notes"`
	proxy          *url.URL
	dns            string
	resolveHost    string
}

func nonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}

func (s Snapshot) Plan(now time.Time) Plan {
	p := Plan{ID: nonce(), Target: "https://" + TargetHost + "/", ExpiresAt: now.Add(60 * time.Second), TimeoutSeconds: 12, Route: "direct", Recipients: []string{}, Operations: []string{}, Data: []string{"无凭据 HEAD /：固定 Host、User-Agent、Connection: close；不发送 Cookie、API key、账户或模型正文。", "TLS 发送目标主机名 SNI、协商参数及网络连接源地址。"}, Notes: []string{"一次确认只允许执行本清单一次；无重试、不跟随重定向、不请求模型。", "DNS 是 /etc/resolv.conf 首个有效服务器的单次 A 查询参考，不代表 macOS split DNS 或 Claude DNS。", "DNS 服务器与代理可能继续转发请求；本清单限定本工具的直接接收方，不能枚举其上游。", "失败即停止后续步骤；不以 DNS 失败推断 Claude 不可用。"}, proxy: s.route.Proxy, resolveHost: TargetHost}
	if now.Sub(s.Report.ObservedAt) > 2*time.Minute || now.Before(s.Report.ObservedAt) {
		p.Reason = "本地证据已过期，请重新只读检查。"
		return p
	}
	if s.route.Blocked {
		p.Reason = "当前配置、provider、代理、证书或平台策略无法由首版安全确认；未生成可执行网络清单。"
		return p
	}
	if p.proxy != nil {
		if literal := net.ParseIP(p.proxy.Hostname()); literal != nil && !literal.IsLoopback() && !publicIPv4(literal.String()) {
			p.Reason = "所选代理为未支持的非公网/特殊用途地址；本版仅接受明确列出的字面回环代理或公网 IPv4 代理。"
			return p
		}
		p.Route = "http_connect"
		p.resolveHost = p.proxy.Hostname()
		p.Recipients = append(p.Recipients, "代理："+p.proxy.Scheme+"://"+proxyAddress(p.proxy))
		p.Operations = append(p.Operations, "向所列代理建立 1 条 TCP 连接，发送 1 次 CONNECT api.anthropic.com:443；代理负责目标解析/转发。")
		if p.proxy.Scheme == "https" {
			p.Operations = append(p.Operations, "对代理进行 1 次 TLS 握手，验证其证书与主机名。")
		}
	}
	if net.ParseIP(p.resolveHost) == nil {
		if s.resolverConfig == nil {
			p.Reason = "缺少明确的 DNS 参考服务器。"
			return p
		}
		data, err := s.resolverConfig()
		if err != nil {
			p.Reason = "不能读取 DNS 参考服务器；没有发起请求。"
			return p
		}
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
				p.dns = f[1]
				break
			}
		}
		if p.dns == "" {
			p.Reason = "未找到明确的 DNS 服务器 IP；没有发起请求。"
			return p
		}
		p.Recipients = append(p.Recipients, "DNS 参考服务器："+net.JoinHostPort(p.dns, "53")+" / UDP")
		p.Operations = append(p.Operations, "最多 1 个 UDP DNS A 查询数据包："+p.resolveHost+".；禁止第二次发送与 TCP 回退。")
		p.Data = append(p.Data, "DNS 查询名及网络连接源地址将发送给清单中的 DNS 服务器。")
	}
	if p.proxy == nil {
		p.Recipients = append(p.Recipients, "固定目标："+TargetHost+":443（从本次 A 回答取第一个 IPv4，不尝试其他地址）")
		p.Operations = append(p.Operations, "向固定目标建立 1 条 TCP 连接。")
	} else {
		p.Recipients = append(p.Recipients, "通过所列代理到达的固定目标："+TargetHost+":443")
	}
	p.Operations = append(p.Operations, "对固定目标进行 1 次 TLS 握手，强制证书与主机名验证。", "发送 1 个 HEAD /；只读取响应头（上限 16 KiB），不读取响应正文。")
	p.Allowed = true
	return p
}

func proxyAddress(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// NetworkIO permits isolated local fixtures without touching external addresses.
// The production CLI never accepts target, resolver or root overrides from HTTP input.
type NetworkIO struct {
	Resolve func(context.Context, string, string) ([]net.IP, error)
	Dial    func(context.Context, string, string) (net.Conn, error)
	Roots   *x509.CertPool
	Now     func() time.Time
	TrustID string // Digest of the exact explicit reference roots, never credentials.
}

func SystemNetwork() NetworkIO {
	// A non-system pool keeps verification inside Go. On macOS a nil/system
	// pool delegates to SecTrust, whose certificate fetching is outside our budget.
	var roots *x509.CertPool
	trustID := ""
	if f, err := os.Open("/etc/ssl/cert.pem"); err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 2*1024*1024+1))
		_ = f.Close()
		if readErr == nil && len(data) <= 2*1024*1024 {
			candidate := x509.NewCertPool()
			if candidate.AppendCertsFromPEM(data) {
				roots = candidate
				trustID = digest(data)
			}
		}
	}
	return NetworkIO{Resolve: resolveOnce, Dial: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, Roots: roots, Now: time.Now, TrustID: trustID}
}

type oneDatagram struct {
	*net.UDPConn
	writes atomic.Int32
}

func (c *oneDatagram) Write(p []byte) (int, error) {
	if c.writes.Add(1) != 1 {
		return 0, errors.New("dns_budget_exhausted")
	}
	return c.UDPConn.Write(p)
}

func (c *oneDatagram) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.writes.Add(1) != 1 {
		return 0, errors.New("dns_budget_exhausted")
	}
	return c.UDPConn.WriteTo(p, addr)
}

// The resolver may attempt a fallback, but this adapter allows at most one dial
// and one UDP write. LookupIP with ip4 and a trailing dot avoids AAAA/search suffixes.
func resolveOnce(ctx context.Context, server, host string) ([]net.IP, error) {
	return resolveAt(ctx, net.JoinHostPort(server, "53"), host)
}

func resolveAt(ctx context.Context, serverAddress, host string) ([]net.IP, error) {
	var dials atomic.Int32
	var datagram *oneDatagram
	r := net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "udp" || dials.Add(1) != 1 {
			return nil, errors.New("dns_budget_exhausted")
		}
		c, err := (&net.Dialer{}).DialContext(ctx, "udp", serverAddress)
		if err != nil {
			return nil, err
		}
		udp, ok := c.(*net.UDPConn)
		if !ok {
			_ = c.Close()
			return nil, errors.New("dns_transport_unavailable")
		}
		datagram = &oneDatagram{UDPConn: udp}
		return datagram, nil
	}}
	ips, err := r.LookupIP(ctx, "ip4", strings.TrimSuffix(host, ".")+".")
	if err == nil && (datagram == nil || datagram.writes.Load() != 1) {
		return nil, errors.New("reference_dns_not_queried")
	}
	return ips, err
}

func classify(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return "certificate_untrusted"
	}
	var name x509.HostnameError
	if errors.As(err, &name) {
		return "certificate_hostname"
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return "certificate_invalid"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection_closed"
	}
	return "connection_error"
}

func tlsConnect(ctx context.Context, c net.Conn, host string, roots *x509.CertPool) (net.Conn, error) {
	if roots == nil {
		return nil, errors.New("reference_trust_unavailable")
	}
	t := tls.Client(c, &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err := t.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// A late successful response cannot authorize a new write after cancellation.
// Bytes already in an in-flight system call cannot be recalled.
type activityConn struct {
	net.Conn
	ctx context.Context
}

func (c *activityConn) Write(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func readHeaders(c net.Conn, req *http.Request) (*http.Response, error) {
	return http.ReadResponse(bufio.NewReader(io.LimitReader(c, 16384)), req)
}

// Probe consumes one immutable server-generated plan. It does no automatic
// retry, redirect, extra DNS lookup, body read, auth or model request.
func (s Snapshot) Probe(parent context.Context, p Plan, n NetworkIO) Report {
	r := s.Report
	r.Checks = append([]Check(nil), s.Report.Checks...)
	r.Mode = "probe"
	r.Probe = &ProbeExecution{State: "not_started", Code: "plan_not_started"}
	r.ReferenceProfile = ""
	if n.TrustID != "" {
		r.ReferenceProfile = digest([]byte("reference-v1|HEAD /|" + TargetHost + "|TLS1.2+|12s|single-attempt|" + n.TrustID))
	}
	now := n.Now().UTC()
	r.ObservedAt = now
	if !p.Allowed || p.ID == "" || !now.Before(p.ExpiresAt) || p.Target != "https://"+TargetHost+"/" {
		if p.Allowed && p.ID != "" && !now.Before(p.ExpiresAt) {
			r.Probe.State = "expired"
			r.Probe.Code = "plan_expired"
		} else {
			r.Probe.Code = "plan_invalid"
		}
		r.set("connection", Unknown, "plan_invalid", "清单不可执行或已过期；没有发起请求。", "consent gate", now, nil)
		return r
	}
	if parent.Err() != nil {
		r.Probe.State = "cancelled"
		r.Probe.Code = "probe_cancelled"
		r.set("connection", Unknown, "probe_cancelled", "调用者已取消；没有发起请求。", "consent gate", now, nil)
		return r
	}
	if n.Roots == nil {
		r.Probe.Code = "reference_trust_unavailable"
		r.set("certificates", Unknown, "reference_trust_unavailable", "固定公开 CA PEM 文件不可用；不调用系统信任服务，也没有发起外联。", "fixed public CA PEM", now, nil)
		r.set("connection", Unknown, "reference_trust_unavailable", "本地参考信任无法准备；没有发起外联。", "consent gate", now, nil)
		return r
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	steps := []Evidence{}
	startAttempt := func() bool {
		if ctx.Err() != nil {
			return false
		}
		if r.Probe.StartedAt == nil {
			at := n.Now().UTC()
			r.Probe.StartedAt = &at
		}
		r.Probe.State = "running"
		return true
	}
	add := func(id string, state Status, code, detail string) {
		steps = append(steps, Evidence{id, state, code, detail, "bounded reference probe", n.Now().UTC()})
	}
	finish := func(state Status, code, detail string) Report {
		switch state {
		case Pass:
			r.Probe.State = "completed"
		case Fail:
			r.Probe.State = "failed"
		default:
			r.Probe.State = "blocked"
		}
		if parent.Err() != nil {
			r.Probe.State = "cancelled"
			state = Unknown
			code = "probe_cancelled"
			detail = "调用者已结束本次参考检查；不据此判断网络失败。"
		}
		r.Probe.Code = code
		r.set("connection", state, code, detail, "bounded reference probe", n.Now().UTC(), steps)
		return r
	}
	address := p.resolveHost
	if net.ParseIP(address) == nil {
		if !startAttempt() {
			return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
		}
		dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
		r.Probe.Attempts.DNS++
		ips, err := n.Resolve(dctx, p.dns, p.resolveHost)
		dcancel()
		if err != nil || len(ips) == 0 {
			add("reference_dns", Unknown, "reference_dns_unavailable", "限定 DNS 参考查询没有获得可用回答；系统及 Claude 的实际解析仍未知。")
			return finish(Unknown, "reference_dns_unavailable", "参考解析不足以继续；没有判断 Claude 链路失败。")
		}
		address = ""
		for _, ip := range ips {
			if !publicIPv4(ip.String()) {
				add("reference_dns", Unknown, "resolved_address_disallowed", "参考解析包含未授权的非公网、特殊用途或不支持的地址；没有拨号。")
				return finish(Unknown, "resolved_address_disallowed", "已拒绝未授权地址；未把本机代理例外扩展到域名解析结果。")
			}
			if address == "" && ip.To4() != nil {
				address = ip.String()
			}
		}
		if address == "" {
			return finish(Unknown, "ipv4_reference_unavailable", "本次参考无 IPv4；不推断 IPv6 或 Claude 连接失败。")
		}
		add("reference_dns", Pass, "reference_answer", "限定 DNS 参考服务器返回了 A 记录；不证明系统或 Claude DNS 路径。")
	} else {
		add("reference_dns", NotApplicable, "literal_proxy_address", "代理使用字面 IP；本次不需要本地 DNS 查询。")
	}
	// The only local-address exception is the exact literal proxy shown in the
	// approved manifest. A domain resolving to loopback never acquires this exception.
	literalProxy := p.proxy != nil && net.ParseIP(p.proxy.Hostname()) != nil && net.ParseIP(p.proxy.Hostname()).Equal(net.ParseIP(address)) && net.ParseIP(address).IsLoopback()
	if !literalProxy && !publicIPv4(address) {
		return finish(Unknown, "resolved_address_disallowed", "目的地址不属于本清单允许的公网范围；没有拨号。")
	}
	port := "443"
	if p.proxy != nil {
		_, port, _ = net.SplitHostPort(proxyAddress(p.proxy))
	}
	if !startAttempt() {
		return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
	}
	dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
	r.Probe.Attempts.TCP++
	conn, err := n.Dial(dctx, "tcp", net.JoinHostPort(address, port))
	dcancel()
	if err != nil {
		code := classify(err)
		add("tcp", Fail, code, "本次参考 TCP 连接失败。")
		return finish(Fail, code, "固定清单中的参考 TCP 连接失败；不代表账户状态。")
	}
	conn = &activityConn{Conn: conn, ctx: ctx}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	// Cancellation closes the live socket as well as respecting its total deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	add("tcp", Pass, "connected", "已建立本次清单中的 TCP 连接；未公开地址。")
	active := conn
	if p.proxy != nil {
		if p.proxy.Scheme == "https" {
			if !startAttempt() {
				return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
			}
			r.Probe.Attempts.ProxyTLS++
			active, err = tlsConnect(ctx, active, p.proxy.Hostname(), n.Roots)
			if err != nil {
				code := classify(err)
				add("proxy_tls", Fail, code, "代理 TLS 验证失败。")
				return finish(Fail, code, "参考代理 TLS 失败。")
			}
			add("proxy_tls", Pass, "verified", "本工具证书库验证了代理 TLS。")
		}
		connect := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: TargetHost + ":443"}, Host: TargetHost + ":443", Header: make(http.Header)}
		if !startAttempt() {
			return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
		}
		r.Probe.Attempts.Connect++
		if err = connect.Write(active); err != nil {
			return finish(Fail, classify(err), "发送固定 CONNECT 请求失败。")
		}
		response, e := readHeaders(active, connect)
		if e != nil {
			return finish(Fail, classify(e), "未收到有效 CONNECT 响应头。")
		}
		if response.StatusCode != http.StatusOK {
			code := "proxy_connect_rejected"
			if response.StatusCode == 407 {
				code = "proxy_auth_required"
			}
			add("connect", Fail, code, "代理未接受 CONNECT；未发送认证信息。")
			return finish(Fail, code, "代理参考隧道未建立。")
		}
		add("connect", Pass, "tunnel_open", "代理接受固定目标 CONNECT；目标解析由代理负责，未单独验证其 DNS。")
		add("target_dns", NotApplicable, "proxy_resolves_target", "CONNECT 路径不执行本地目标 DNS；远端解析没有独立证据。")
	}
	if !startAttempt() {
		return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
	}
	r.Probe.Attempts.TargetTLS++
	active, err = tlsConnect(ctx, active, TargetHost, n.Roots)
	if err != nil {
		code := classify(err)
		add("tls", Fail, code, "固定目标 TLS 验证失败。")
		return finish(Fail, code, "参考链路未通过本工具 TLS 校验。")
	}
	add("tls", Pass, "verified", "固定目标证书与主机名通过本工具固定公开 CA PEM 策略；未证明 Claude 运行时结果。")
	req := &http.Request{Method: http.MethodHead, URL: &url.URL{Path: "/"}, Host: TargetHost, Header: http.Header{"User-Agent": []string{"cc-health/1"}}, Close: true}
	if !startAttempt() {
		return finish(Unknown, "probe_cancelled", "活动授权已结束；没有开始后续网络阶段。")
	}
	r.Probe.Attempts.Head++
	if err = req.Write(active); err != nil {
		return finish(Fail, classify(err), "发送固定 HEAD 请求失败。")
	}
	response, err := readHeaders(active, req)
	if err != nil {
		return finish(Fail, classify(err), "未收到有效 HTTP 响应头。")
	}
	// No response body, headers, URL or reason phrase is copied into the report.
	status := response.StatusCode
	detail := fmt.Sprintf("收到 HTTP %d；只证明本次 HTTP 响应到达，认证、地区资格、额度和模型均未验证。", status)
	if status >= 500 || status == 429 {
		add("http", Fail, "reference_service_response", detail)
		return finish(Fail, "reference_service_response", detail)
	}
	if status < 200 {
		add("http", Unknown, "interim_response", detail)
		return finish(Unknown, "interim_response", detail)
	}
	add("http", Pass, "response_received", detail)
	return finish(Pass, "reference_reachable", detail)
}
