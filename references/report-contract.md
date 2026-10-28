# 第一阶段报告契约

实现：`internal/health/report.go`、`local.go`、`probe.go`。本协议是独立环境证据策略，不是 Claude、Net.Coffee 或服务方账号算法。

## 字段与状态

- `mode`: `local` 或 `probe`。静态 demo 不使用此报告管线；测试 fixture 不能据此认作真实设备验收。
- `scope`: 当前启动环境 + 固定目标的受控参考链路。不是已运行 Claude 的最终环境或模型调用。
- `observed_at`: 本次采样时间；每个 check/step 另有自己的时间，不把旧本地证据改写为新采样。
- `checks`: `id/title/status/code/detail/source/observed_at/steps`。描述与原因码来自固定模板，不包含完整配置、路径、地址、HTTP headers/body 或原始错误。
- `score`: `value` 为整数或 `null`；有 `scope/unverified/policy/coverage/passed_weight/judged_weight/applicable_weight/reasons`。`scope` 明确限定本次参考链路评分，`unverified` 紧邻列出实际 Claude 路由与有效配置、账户与额度、模型未验证；100 分也不表示整机或 Claude 可用性通过。页面在分数旁始终显示该范围，不能只放进折叠明细。
- `limitations`: 未验证范围。`repair_available` 只表示当前页面接入新会话适配器，不是执行授权或保证可启动。`reference_profile` 是固定方法/目标/预算策略与实际参考 CA 的摘要，缺失时不声称跨次可比。

状态为 `pass`（该明确范围有通过证据）、`fail`（该检查已确认异常）、`unknown`（证据不足、权限/能力受限或未执行）、`not_applicable`（子步骤确实不适用，例如 CONNECT 不执行本地目标解析）。超时为对应步骤原因码，不把超时当通过。

本地读取与报告输出是不同边界：完整环境副本和 settings 原文可能含敏感值，会进入本地内存作必要解析。会话基线持有 settings 全文摘要、允许列表环境摘要、元数据及执行/路由上下文；不发送、记录或导出原文，也不导出这些绑定摘要。`reference_profile` 则是参考方法/公开 CA 摘要，允许进入报告。不能把“报告无原文”解释成“从未读取凭据值”。

## 评分策略 shell-reference-v1

| ID | 权重 | 通过依据 |
| --- | ---: | --- |
| installation | 20 | 找到 CLI，禁网禁写沙箱内版本输出符合已知格式 |
| connection | 35 | 本清单固定路径的 TCP/TLS/HTTP 证据；CONNECT 适用时也通过 |
| proxy | 25 | 当前启动环境的代理选择和 NO_PROXY 规则可明确确定；不代表已运行进程 |
| configuration | 10 | 固定用户 JSON/env 基础结构与常见来源检查没有未检查的存在项或读取失败；最终多层配置另列未知 |
| certificates | 10 | 当前没有不支持的自定义/关闭验证策略；本工具始终强制固定公开 CA PEM 验证。探测时不能准备该池则改为未知 |

五组对固定 HTTPS 场景均适用，总权重 100。缺组、重复组、无效状态、顶层 `not_applicable` 均按未知处理，不能缩小分母。本版只有证实不适用的**子步骤**可标 NA；它们没有独立权重。

`value = round(100 × passed_weight / applicable_weight)`；`coverage = judged_weight / applicable_weight`，已判断仅包含 pass/fail。未知留在分母，不叫失败，也不当通过。覆盖率 <80%，或 installation/proxy/connection 任何一项未知时，`value=null`。这些关键组确认失败，或基础配置语法失败，会把可显示的数值上限设为 49。

连接 DNS/TCP/TLS/CONNECT/HTTP 只组成 connection 一组，不重复扣分。DNS 参考本身不独立计分；单服务器参考解析失败/无 IPv4 时 connection 为未知，而不是认定 Claude 不可用。字面 IP 代理不需要本地 DNS，CONNECT 的目标 DNS 由代理负责，均保留适用条件。

`account` 与 `effective_configuration` 永远 unknown、无通过权重；它们不属于这个参考评分的适用集合。分数不是账户或模型分数。

## 解释 HTTP 与 TLS

200/3xx/401/403/404 等最终 HTTP 响应可证明本次 HTTP 到达；不跟随 Location，不读取正文，也不推断身份、地区资格或模型成功。429/5xx 为本次参考服务异常，不推断账号额度。407 说明本次代理要求认证；不使用或发送代理认证信息；其启动环境值可能已进入本地内存。TLS 信任、主机名和证书有效期错误分别记录；错误文本和证书身份不输出。

固定 `/etc/ssl/cert.pem` 是参考信任库，和 Claude 的 bundled Mozilla/OS/Node 组合不同。使用显式 `x509.NewCertPool` 避免隐式 macOS 信任服务网络请求；缺少可用 PEM 时不发起探测。

## 新一轮检查

预览使用不超过 2 分钟的本地快照，清单 60 秒有效且单次消费。取消在服务端与执行互斥地撤销待执行清单；重复取消可确认同一撤销状态，旧 ID 不再能执行。过期只报告过期，已开始或已完成的执行不能声称已撤销。新本地检查使旧清单失效；每次新探测都重新预览和确认。导出的只有 Report，不包括授权清单中的 DNS/代理地址。本阶段可以再次检查，但没有实现修复因果归因、before/after 对比或受控回滚。

## 新会话计划与状态

RepairPlan 独立于 Report，含随机 ID、有效期、明确选择的回环代理、CLI 绝对路径/工作目录、批准文件摘要、环境变更、正常启动副作用和参考网络清单；这些路径在本地批准弹窗展示供用户核对，不进入应用日志或报告导出。API 不接受任意命令、参数、文件路径或目标。

操作状态分别保存 `accepted/starting/running/exited/stopped/cancelled_before_start/invalidated/start_failed`、环境是否交付、根进程退出码、终端是否恢复、参考复测状态/Report、基线和可比性。一次计划只能消费一次，HTTP 断开不解除或重复批准。停止不包含文件或网络回滚，也不保证后代退出。

可比较必须有两分钟内的基线和完全一致的非空 reference_profile；没有参考 CA 身份证据时宁可标不可比。比较只针对固定参考检查，不能将得分提升解读为 Claude 模型已修复。

## 独审修复后的状态证据

`Report.probe` 包含明确 `state/code/started_at/attempts`，attempts 为已实际调用的 DNS/TCP/代理 TLS/CONNECT/目标 TLS/HEAD 尝试次数，不代表远端一定收到了完整请求。未开始时 started_at=null、次数全零。过期为 expired，调用方取消为 cancelled，失败为 failed，证据中止为 blocked；只有成功完成才为 completed。Manager 直接使用该回执，不以函数返回就称完成；跨期不延长授权。可比必须两份回执均成功完成、有实际 TCP/HEAD 尝试且参考 profile 一致。

新会话批准对象是持有的源文件 FD 与目录 FD；执行的是校验后的私有原生副本。清单包含摘要、临时副本限制、独立原生分发要求和同 UID 威胁边界。确认阶段不再调用额外 --version。进程退出与副本清理为独立事实，后者未知时 artifact_retained 不能清零。
