// Package execution applies one approved environment to one new root process.
package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"cc-anti-ban-skill/internal/health"
	"cc-anti-ban-skill/internal/runner"
)

type Process interface {
	View() runner.View
	Done() <-chan struct{}
	Stop()
	RetryCleanup() error
}
type Dependencies struct {
	Recheck     func(context.Context, health.Snapshot) health.Snapshot
	Available   func() bool
	Start       func(*runner.Prepared, []string) (Process, error)
	Environment func() []string
	Network     func() health.NetworkIO
	Now         func() time.Time
	Materialize func(context.Context, *runner.Image) (*runner.Prepared, error)
	Cleanup     func(*runner.Prepared) error
}

type Plan struct {
	ID                 string      `json:"id"`
	Allowed            bool        `json:"allowed"`
	Reason             string      `json:"reason"`
	ExpiresAt          time.Time   `json:"expires_at"`
	Proxy              string      `json:"proxy"`
	Executable         string      `json:"executable,omitempty"`
	Directory          string      `json:"directory,omitempty"`
	ApprovedDigest     string      `json:"approved_digest,omitempty"`
	Changes            []string    `json:"changes"`
	Warnings           []string    `json:"warnings"`
	Reference          health.Plan `json:"reference"`
	BaselineComparable bool        `json:"baseline_comparable"`
	fingerprint        string
	image              *runner.Image
	baseline           health.Report
	candidate          health.Snapshot
}
type Status struct {
	ID                  string         `json:"id"`
	State               string         `json:"state"`
	Code                string         `json:"code,omitempty"`
	Process             *runner.View   `json:"process,omitempty"`
	ReferenceState      string         `json:"reference_state"`
	Reference           *health.Report `json:"reference,omitempty"`
	Baseline            *health.Report `json:"baseline,omitempty"`
	Comparable          bool           `json:"comparable"`
	Limitations         []string       `json:"limitations"`
	Finished            bool           `json:"finished"`
	ArtifactRetained    bool           `json:"artifact_retained"`
	CleanupRetryAllowed bool           `json:"cleanup_retry_allowed"`
	CleanupOnly         bool           `json:"cleanup_only"`
	CleanupCode         string         `json:"cleanup_code,omitempty"`
}

type Manager struct {
	mu       sync.Mutex
	deps     Dependencies
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	status   Status
	process  Process
	retained *runner.Prepared
	done     chan struct{}
	opCancel context.CancelFunc
	consumed map[string]bool
	images   map[string]*runner.Image
}

func New(parent context.Context, d Dependencies) *Manager {
	ctx, cancel := context.WithCancel(parent)
	if d.Materialize == nil {
		d.Materialize = func(ctx context.Context, i *runner.Image) (*runner.Prepared, error) { return i.Materialize(ctx) }
	}
	if d.Cleanup == nil {
		d.Cleanup = func(p *runner.Prepared) error { return p.Close() }
	}
	m := &Manager{deps: d, ctx: ctx, cancel: cancel, consumed: map[string]bool{}, images: map[string]*runner.Image{}}
	context.AfterFunc(ctx, func() { m.Close() })
	return m
}
func token() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}

func (m *Manager) Prepare(s health.Snapshot, host string, port int) Plan {
	now := m.deps.Now()
	p := Plan{ID: token(), ExpiresAt: now.Add(time.Minute), Changes: []string{"仅为新 Claude 进程及其后代设置 HTTP_PROXY/HTTPS_PROXY/http_proxy/https_proxy。", "仅在新进程环境清除 ALL_PROXY/all_proxy/NO_PROXY/no_proxy，避免旧旁路或代理覆盖；这也会改变该进程其他请求的旁路行为。", "不修改原 shell、系统代理或配置文件；拒绝已观察的 DYLD 控制，并在启动边界移除全部 DYLD_*；其他环境（可能含凭据）原样交给新进程；本工具不把这些值写入日志或导出。"}, Warnings: []string{"一次确认将创建并执行已批准入口的私有原生副本，并按仍有效的清单进行一次参考复测；请回到原终端交互，无需另跑命令。确认后仅安全复核对象与配置，不再额外执行 --version。", "正常 Claude 启动不在版本沙箱内，可能自行联网、写文件、运行 hooks/MCP；这部分不受下方参考探测预算约束，也不由工具保证次数，停止进程不能撤销已有副作用。", "仅支持原生 Mach-O 入口与已识别的系统动态库布局；脚本、npm shim、rpath 或已知外部依赖布局被拒绝。临时位置可能影响自更新和相对资源路径，不保证所有运行时依赖兼容。", "本次持有源文件/目录对象，普通原安装路径更新不会替换批准入口；源内容原位变化会因摘要不符拒绝。公开接口不提供执行 FD 固定，这不是对任意同 UID 恶意篡改私有副本的完整防护。", "project/managed/remote/MDM 最终配置仍可能覆盖本次变量，实际 Claude 路由、账户与模型未验证。", "结束/到期关闭本机服务会请求停止本次拥有的 Claude 根进程，必要时有界强制终止；不承诺收回全部后代或回滚文件/网络。", "CLI 输出与输入只在原终端，不进入网页、日志或导出；网页只显示进程状态与参考证据。终端 Ctrl-C 由内核发给整个前台进程组，可能影响同组其他作业；程序停止只针对仍由唯一监督者持有的根 PID，不向组发信号。启动过新会话后结束服务请用页面或弹窗内按钮，或等待 15 分钟到期。"}}
	if host != "127.0.0.1" && host != "::1" || port < 1 || port > 65535 {
		p.Reason = "仅支持明确选择的字面回环 HTTP 代理和有效端口。"
		return p
	}
	p.Proxy = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	if m.Active() || !m.deps.Available() {
		p.Reason = "当前本机原生启动适配器或前台交互终端不可用，或已有本次进程；请使用支持本机的平台构建并在交互终端运行。"
		return p
	}
	if now.Before(s.Report.ObservedAt) || now.Sub(s.Report.ObservedAt) > 2*time.Minute {
		p.Reason = "本地证据已过期，请重新检查。"
		return p
	}
	candidate, reason := s.CandidateReference(p.Proxy, now)
	if reason != "" {
		p.Reason = "存在已观察的冲突、未知目标或不可用上下文，未授权启动：" + reason
		return p
	}
	path, dir := s.Command()
	bindCtx, bindCancel := context.WithTimeout(m.ctx, 2*time.Second)
	image, err := runner.OpenImage(bindCtx, path, dir)
	bindCancel()
	if err != nil {
		p.Reason = "无法可靠持有支持的独立原生 Mach-O 入口/目录；脚本、未知 shim 与已知外部布局不在支持范围，没有启动进程。"
		return p
	}
	p.Reference = candidate.Plan(now)
	if !p.Reference.Allowed {
		image.Close()
		p.Reason = p.Reference.Reason
		return p
	}
	p.image = image
	image.ExpireAfter(time.Minute)
	p.Executable = image.Path
	p.Directory = image.Directory
	p.ApprovedDigest = image.Digest
	p.fingerprint = s.Fingerprint()
	p.candidate = candidate
	p.baseline = s.Report
	p.BaselineComparable = s.Report.ReferenceCompleted() && s.Report.Score.Policy == health.PolicyVersion && s.Report.ReferenceProfile != ""
	p.Allowed = true
	m.mu.Lock()
	if m.closed {
		image.Close()
		p.Allowed = false
		p.Reason = "本机服务已关闭。"
	} else {
		m.images[p.ID] = image
	}
	m.mu.Unlock()
	return p
}

func (p Plan) Close() {
	if p.image != nil {
		p.image.Close()
	}
}

func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeLocked() || m.cleanupPendingLocked()
}
func (m *Manager) activeLocked() bool {
	if m.done == nil {
		return false
	}
	select {
	case <-m.done:
		return false
	default:
		return true
	}
}

// Accept is called only after the HTTP session atomically consumes its plan.
func (m *Manager) Accept(p Plan) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.activeLocked() || m.cleanupPendingLocked() || p.ID == "" || m.consumed[p.ID] || !p.Allowed || p.image == nil || !m.deps.Now().Before(p.ExpiresAt) {
		return Status{}, errors.New("plan_unavailable")
	}
	if !p.image.Consume() {
		return Status{}, errors.New("approved_objects_unavailable")
	}
	m.consumed[p.ID] = true
	m.process = nil
	m.done = make(chan struct{})
	opCtx, opCancel := context.WithCancel(m.ctx)
	m.opCancel = opCancel
	m.status = Status{ID: p.ID, State: "accepted", ReferenceState: "not_started", Comparable: false, Limitations: []string{"指定环境交付不证明 Claude 实际最终路由。", "账号、额度、模型未验证；分数变化只反映参考证据。", "停止只针对本次根进程，不能撤销已发生的文件/网络或保证全部后代退出。"}}
	go m.execute(opCtx, p, m.done)
	return m.status, nil
}

func (m *Manager) execute(opCtx context.Context, p Plan, done chan struct{}) {
	defer close(done)
	defer p.Close()
	ctx, cancel := context.WithTimeout(opCtx, 10*time.Second)
	defer cancel()
	fresh := m.deps.Recheck(ctx, p.candidate)
	_, conflict := fresh.CandidateReference(p.Proxy, m.deps.Now())
	var prepared *runner.Prepared
	var err error
	if fresh.Fingerprint() == p.fingerprint && conflict == "" {
		prepared, err = m.deps.Materialize(ctx, p.image)
	}
	m.mu.Lock()
	if m.closed || opCtx.Err() != nil || m.status.State == "cancelled_before_start" {
		if prepared != nil {
			m.cleanupPreparedLocked(prepared)
		}
		m.status.State = "cancelled_before_start"
		m.mu.Unlock()
		return
	}
	if err != nil || prepared == nil || fresh.Fingerprint() != p.fingerprint || conflict != "" || !m.deps.Now().Before(p.ExpiresAt) || !m.deps.Available() {
		if prepared != nil {
			m.cleanupPreparedLocked(prepared)
		}
		m.status.State = "invalidated"
		m.status.Code = "launch_evidence_changed"
		m.mu.Unlock()
		return
	}
	m.status.State = "starting"
	// Holding this short commit lock makes Stop truthful around OS Start.
	process, startErr := m.deps.Start(prepared, Overlay(m.deps.Environment(), p.Proxy))
	if startErr != nil {
		m.cleanupPreparedLocked(prepared)
		m.status.State = "start_failed"
		m.status.Code = "process_start_failed"
		m.mu.Unlock()
		return
	}
	m.process = process
	m.status.State = "running"
	m.status.ReferenceState = "running"
	baseline := p.baseline
	m.status.Baseline = &baseline
	m.mu.Unlock()
	// Independent of the HTTP request; only explicit service shutdown cancels it.
	r := p.candidate.Probe(opCtx, p.Reference, m.deps.Network())
	r.RepairAvailable = true
	m.mu.Lock()
	m.status.Reference = &r
	m.status.Comparable = p.BaselineComparable && r.ReferenceCompleted() && r.ReferenceProfile != "" && p.baseline.ReferenceProfile == r.ReferenceProfile && opCtx.Err() == nil
	m.status.ReferenceState = "not_started"
	if r.Probe != nil {
		m.status.ReferenceState = r.Probe.State
	}
	if opCtx.Err() != nil {
		m.status.ReferenceState = "cancelled"
	}
	m.mu.Unlock()
	<-process.Done()
	m.mu.Lock()
	if !process.View().ArtifactRetained {
		m.cleanupPreparedLocked(prepared)
	}
	m.status.State = process.View().State
	m.mu.Unlock()
}

func (m *Manager) Status(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" || id != m.status.ID {
		return Status{}, errors.New("not_current")
	}
	return m.statusLocked(), nil
}
func (m *Manager) statusLocked() Status {
	s := m.status
	s.ArtifactRetained = m.cleanupPendingLocked()
	if m.retained != nil {
		s.CleanupCode = m.retained.CleanupCode()
		if s.CleanupCode == "" {
			s.CleanupCode = "private_copy_cleanup_failed"
		}
	}
	s.CleanupRetryAllowed = s.ArtifactRetained && !m.activeLocked() && (m.process == nil || m.process.View().ExitCode != nil)
	s.CleanupOnly = m.closed
	s.Finished = !m.activeLocked()
	if m.process != nil {
		v := m.process.View()
		s.Process = &v
		if s.CleanupCode == "" {
			s.CleanupCode = v.CleanupCode
		}
		if v.State == "exited" || v.State == "stopped" {
			s.State = v.State
		}
	}
	return s
}

// Each recovery target originates from this manager's consumed plan; clients
// supply only its opaque nonce, never a pathname or PID. Keep it until cleanup.
func (m *Manager) cleanupPreparedLocked(p *runner.Prepared) {
	if m.deps.Cleanup(p) != nil {
		m.retained = p
	} else if m.retained == p {
		m.retained = nil
	}
}
func (m *Manager) cleanupPendingLocked() bool {
	return m.retained != nil || (m.process != nil && m.process.View().ArtifactRetained)
}
func (m *Manager) ResourcesPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeLocked() || m.cleanupPendingLocked()
}
func (m *Manager) RetryCleanup(id string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" || id != m.status.ID || m.activeLocked() {
		return Status{}, errors.New("cleanup_unavailable")
	}
	if m.retained != nil {
		m.cleanupPreparedLocked(m.retained)
	}
	if m.process != nil && m.process.View().ArtifactRetained {
		_ = m.process.RetryCleanup()
	}
	// A failed deletion remains a retryable state, not a discarded error/object.
	return m.statusLocked(), nil
}
func (m *Manager) CurrentID() string { m.mu.Lock(); defer m.mu.Unlock(); return m.status.ID }
func (m *Manager) Stop(id string) (Status, error) {
	m.mu.Lock()
	if id == "" || id != m.status.ID {
		m.mu.Unlock()
		return Status{}, errors.New("not_current")
	}
	if m.process == nil && m.status.State == "accepted" {
		m.status.State = "cancelled_before_start"
	}
	p := m.process
	if m.opCancel != nil {
		m.opCancel()
	}
	m.mu.Unlock()
	if p != nil {
		p.Stop()
	}
	return m.Status(id)
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	p := m.process
	images := make([]*runner.Image, 0, len(m.images))
	for _, image := range m.images {
		images = append(images, image)
	}
	if p == nil && m.status.State == "accepted" {
		m.status.State = "cancelled_before_start"
	}
	m.mu.Unlock()
	m.cancel()
	for _, image := range images {
		image.Close()
	}
	if p != nil {
		p.Stop()
	}
}
func (m *Manager) Wait(ctx context.Context) bool {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		m.mu.Lock()
		p := m.process
		m.mu.Unlock()
		return (p == nil || p.View().ExitCode != nil) && !m.ResourcesPending()
	case <-ctx.Done():
		return false
	}
}

var proxyKeys = map[string]bool{"HTTP_PROXY": true, "HTTPS_PROXY": true, "http_proxy": true, "https_proxy": true, "ALL_PROXY": true, "all_proxy": true, "NO_PROXY": true, "no_proxy": true}

func Overlay(original []string, proxy string) []string {
	out := make([]string, 0, len(original)+4)
	for _, entry := range original {
		key, _, _ := strings.Cut(entry, "=")
		if !proxyKeys[key] && !strings.HasPrefix(key, "DYLD_") {
			out = append(out, entry)
		}
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		out = append(out, key+"="+proxy)
	}
	return out
}
