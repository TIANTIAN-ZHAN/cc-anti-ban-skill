// Package health collects bounded, read-only evidence. A probe is not a Claude model call.
package health

import (
	"math"
	"time"
)

type Status string

const (
	Pass          Status = "pass"
	Fail          Status = "fail"
	Unknown       Status = "unknown"
	NotApplicable Status = "not_applicable"
	PolicyVersion        = "shell-reference-v1"
)

type Evidence struct {
	ID         string    `json:"id"`
	Status     Status    `json:"status"`
	Code       string    `json:"code"`
	Detail     string    `json:"detail"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

type Check struct {
	Evidence
	Title string     `json:"title"`
	Steps []Evidence `json:"steps,omitempty"`
}

type Score struct {
	Value            *int     `json:"value"`
	Scope            string   `json:"scope"`
	Unverified       []string `json:"unverified"`
	Policy           string   `json:"policy"`
	Coverage         float64  `json:"coverage"`
	PassedWeight     int      `json:"passed_weight"`
	JudgedWeight     int      `json:"judged_weight"`
	ApplicableWeight int      `json:"applicable_weight"`
	Reasons          []string `json:"reasons"`
}

type Report struct {
	Mode             string          `json:"mode"`
	Scope            string          `json:"scope"`
	ObservedAt       time.Time       `json:"observed_at"`
	Checks           []Check         `json:"checks"`
	Score            Score           `json:"score"`
	Limitations      []string        `json:"limitations"`
	RepairAvailable  bool            `json:"repair_available"`
	ReferenceProfile string          `json:"reference_profile,omitempty"`
	Probe            *ProbeExecution `json:"probe,omitempty"`
}

type ProbeAttempts struct {
	DNS       int `json:"dns"`
	TCP       int `json:"tcp"`
	ProxyTLS  int `json:"proxy_tls"`
	Connect   int `json:"connect"`
	TargetTLS int `json:"target_tls"`
	Head      int `json:"head"`
}
type ProbeExecution struct {
	State     string        `json:"state"`
	Code      string        `json:"code"`
	StartedAt *time.Time    `json:"started_at"`
	Attempts  ProbeAttempts `json:"attempts"`
}

func (r Report) ReferenceCompleted() bool {
	return r.Probe != nil && r.Probe.State == "completed" && r.Probe.Attempts.TCP > 0 && r.Probe.Attempts.Head > 0
}

var policy = []struct {
	id       string
	weight   int
	critical bool
}{
	{"installation", 20, true}, {"connection", 35, true}, {"proxy", 25, true},
	{"configuration", 10, false}, {"certificates", 10, false},
}

// Evaluate is a deterministic pure function. The policy, not caller-supplied weights,
// determines applicability. All five groups apply to the fixed HTTPS reference target.
// NA is allowed in evidence steps (e.g. target DNS through CONNECT), not these groups.
// Missing/duplicate/invalid/NA groups remain unknown, never silently leave the denominator.
func Evaluate(checks []Check) Score {
	s := Score{Scope: "本次检查范围内的参考链路评分", Unverified: []string{"Claude 实际路由及 project/managed/remote/MDM 有效配置", "登录、账户、地区资格与额度", "模型响应与可用性"}, Policy: PolicyVersion, ApplicableWeight: 100, Reasons: []string{}}
	criticalUnknown, criticalFail := false, false
	for _, p := range policy {
		state, count := Unknown, 0
		for _, c := range checks {
			if c.ID == p.id {
				state = c.Status
				count++
			}
		}
		if count != 1 {
			state = Unknown
		}
		if state == Pass || state == Fail {
			s.JudgedWeight += p.weight
		}
		if state == Pass {
			s.PassedWeight += p.weight
		}
		if p.critical && state != Pass && state != Fail {
			criticalUnknown = true
		}
		if (p.critical || p.id == "configuration") && state == Fail {
			criticalFail = true
		}
	}
	s.Coverage = float64(s.JudgedWeight) / float64(s.ApplicableWeight)
	if s.Coverage < .8 {
		s.Reasons = append(s.Reasons, "coverage_below_80_percent")
	}
	if criticalUnknown {
		s.Reasons = append(s.Reasons, "critical_evidence_unknown")
	}
	if len(s.Reasons) > 0 {
		return s
	}
	value := int(math.Round(100 * float64(s.PassedWeight) / float64(s.ApplicableWeight)))
	if criticalFail && value > 49 {
		value = 49
		s.Reasons = append(s.Reasons, "critical_failure_cap_49")
	}
	s.Value = &value
	return s
}

func (r *Report) set(id string, state Status, code, detail, source string, at time.Time, steps []Evidence) {
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			r.Checks[i].Evidence = Evidence{id, state, code, detail, source, at}
			r.Checks[i].Steps = steps
		}
	}
	r.Score = Evaluate(r.Checks)
}

func newReport(at time.Time) Report {
	r := Report{Mode: "local", Scope: "macOS + Claude Code：当前启动环境与固定 HTTPS 参考链路；不是已运行 Claude 的有效配置或模型可用性", ObservedAt: at,
		Limitations: []string{"未验证登录、账户、地区资格、额度、模型响应或账号处置。", "本地检查会读取环境副本与 settings.json 原文，并在会话内保留摘要和必要上下文；报告/导出不含原文、凭据或个人路径。项目/managed 仅查元数据，remote/MDM 未验证。", "Go 的固定公开 CA PEM 信任与 Claude 自带 Mozilla/Node/原生运行时可能不同。", "主动探测只检验清单中的限定路径，不等同 macOS split DNS、系统代理或已运行 Claude 的链路。", "可另行确认仅为新进程应用代理与参考复测；不修改系统或配置文件，不提供完整副作用回滚。"}}
	for _, item := range []struct{ id, title string }{{"installation", "Claude Code 安装与版本"}, {"connection", "固定目标连接"}, {"proxy", "启动环境代理路由"}, {"configuration", "基础配置结构"}, {"certificates", "参考链路证书策略"}, {"account", "账户与模型能力"}, {"effective_configuration", "Claude 有效配置"}} {
		r.Checks = append(r.Checks, Check{Evidence: Evidence{item.id, Unknown, "not_checked", "尚未检查", "none", at}, Title: item.title})
	}
	r.set("account", Unknown, "not_tested", "未发送凭据或模型请求；不纳入通过权重。", "scope", at, nil)
	return r
}
