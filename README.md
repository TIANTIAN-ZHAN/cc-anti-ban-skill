# cc-anti-ban-skill

macOS 上的 Claude Code 本地环境审查工具：查看安装、配置与代理证据，在明确确认后运行一次参考探测，或仅为一个新 Claude 会话设置临时回环 HTTP 代理。

本项目独立开发，**不是 Anthropic 官方产品**。“anti-ban”是仓库名称，不代表防封、恢复账号、地区资格或模型可用性保证。分数是可解释的参考检查结果；未知项不会被算成通过。默认没有主动外联，不修改系统代理、原 shell 或配置文件。

## 开始使用

需要 macOS、Go 1.25+、Xcode 或 Command Line Tools 的 clang/SDK，以及**已经运行、解析与模块可信的 zsh**。核心使用 Go 标准库和 macOS 系统 API，不需要 Node、Python 或 Claude 才能启动界面；没有预编译发行包。

先克隆完整仓库。若目录已存在，不要覆盖它；进入你已有的仓库继续下一步。

```zsh
git clone https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill.git &&
cd -- cc-anti-ban-skill
```

下面整段从工具仓库根目录运行。它只在**本次构建成功**后询问待检查项目目录并启动；失败时不会运行残留的旧二进制。提示出现后输入已有项目的绝对路径，可含空格，不要额外包引号。目录为空或无效时不会启动。

```zsh
(
  cc_health_repo="$PWD" &&
  source "$cc_health_repo/scripts/build-safe.zsh" &&
  read -r "cc_health_project?要检查的项目目录（绝对路径，可含空格）: " &&
  [[ "$cc_health_project" == /* && -d "$cc_health_project" ]] &&
  cd -- "$cc_health_project" &&
  "$cc_health_repo/.build/cc-health" serve
)
```

**`serve` 启动时的当前目录就是被检查的项目目录**，也是后续新 Claude 会话绑定的目录。不要为了方便就在工具仓库里启动后，误以为检查了另一个项目。完整入口在子 shell 内执行；结束后，终端目录和父 shell 中已有或未设置的同名变量均保持原状。

打开终端给出的完整一次性链接，先阅读页面告知，再决定是否同意本地检查。链接包含临时会话凭据，请勿分享。仅监听 `127.0.0.1` 的随机端口；页面刷新不会恢复会话。普通活动授权最多 15 分钟；到期停止活动，若临时副本仍待清理，仅保留原会话的状态和清理重试。

只想输出本地 JSON 时，使用上面同一构建/目录流程，把最后的 `serve` 改为 `check --local-consent`。此参数表示你已同意下述本地读取范围；它不执行主动探测。

## 本地读取与隐私

检查会枚举完整进程环境，并有界读取固定用户 `settings.json` 原文（超过 64 KiB 拒绝）。数据可能含 API key、代理密码等敏感值，会在读取/解析时进入本地内存。程序处理所需字段，会话内保留全文与允许列表环境的摘要、元数据和必要执行/路由信息，不把原文发给外部服务、写入日志或导出；不承诺立即擦除内存。

不会主动打开 Keychain、浏览器 Cookie、聊天或独立凭据文件，也不执行配置中的 hook/helper。DYLD 按名称识别，不在快照中保留或输出值；这不等于完整环境枚举从未接触其值。项目/managed 只检查元数据，remote/MDM 和运行中 Claude 的最终配置未验证。

CLI 绝对路径与工作目录在本地启动确认卡显示，代理/DNS 地址在相应网络确认清单显示，供用户核对；它们不进入应用日志或报告导出。正常获准启动的 Claude 会继承其他环境（可能含凭据），并可自行联网、写文件、执行 hooks/MCP。详见 [隐私与授权边界](references/safety-and-privacy.md)。

## 检查与评分

| 能力 | 实际证据与边界 |
| --- | --- |
| 安装/版本 | PATH 查找；macOS 默认拒绝沙箱内 `claude --version`，禁网禁写、3 秒上限；不可用时未知，不降级为无沙箱运行 |
| 基础配置 | 固定用户 JSON/env 结构校验、全文摘要绑定与必要字段比较；其他常见配置来源只查元数据 |
| 启动代理 | `https_proxy → HTTPS_PROXY → http_proxy → HTTP_PROXY`；显式处理旁路，冲突/未知规则不猜测 |
| 固定参考链路 | 直接连接或 HTTP(S) CONNECT、TLS、无凭据 HEAD；分层保留尝试与结果，不代表真实 Claude 路由或模型调用 |
| 新会话代理 | 仅允许用户选择的 `127.0.0.1`/`::1` HTTP 代理；一次计划绑定源文件、目录、环境与有效期 |

策略 `shell-reference-v1`：安装 20、参考链路 35、启动代理 25、基础配置 10、证书策略 10。总分为通过权重/适用权重，覆盖率为已判断权重/适用权重。覆盖率低于 80% 或关键证据未知时不显示数字；确认的关键失败或配置语法失败最高 49。账户、模型和最终有效配置始终单列未知，不纳入通过权重。详见 [报告契约](references/report-contract.md)。

自定义 provider、认证代理、SOCKS/仅 ALL_PROXY、mTLS、自定义信任或已观察配置冲突会阻断不支持的操作，不通过换目标、发送凭据或降低 TLS 验证兜底。

## 主动探测与新会话

主动外联必须先预览不可编辑的清单，再单次确认。固定目标是 `https://api.anthropic.com/`；最多一次单服务器 UDP A 参考查询（如需要）、一次 TCP/CONNECT、所需 TLS 握手和一个无凭据 HEAD。总超时 12 秒，不重试、不跟随跳转、不读取 HTTP 正文、不调用模型。DNS/代理可能继续转发，清单只限定本工具的直接接收方。401/403/404 仅证明收到响应，不证明账户或地区资格。

新会话确认后才启动批准源 FD 制作的私有 Mach-O 副本，并通过持有的目录 FD 选择工作目录。仅支持本机架构的 thin 64 位 Mach-O 和已识别的系统库布局；脚本/npm shim、fat、多架构、rpath、未知加载命令及已知外部依赖拒绝。临时位置可能影响相对资源和自更新，官方 Claude 的完整兼容性尚未实证。

四个 HTTP(S)_PROXY 变量仅在新进程中设置；ALL_PROXY/NO_PROXY 与 DYLD 项从新环境移除，原 shell 不变。正常 Claude 自身的网络/文件/hooks 不受参考请求预算约束；停止只针对仍由唯一监督者拥有的根 PID，不回滚副作用、不保证清空后代。终端 Ctrl-C 是内核向前台进程组广播，可能影响同组其他作业。启动过交互会话后，用页面结束服务。

清理失败保留对象，阻止新计划覆盖并提供受保护重试。持有目录 FD、最小删除和错误捕获还原用于应对普通路径置换；不递归追删。公开 API 没有 expected-inode 原子删除，随机私有槽不是同 UID 隔离。强制杀进程/断电无跨重启恢复；保持原页面，直到清理完成。

## 构建与支持范围

安全构建在子 shell 内验证 zsh 分派表、按名称清除 DYLD、核后置条件，再本地构建并使用 ad-hoc hardened runtime 签名；成功才发布产物。无需 Developer ID，不执行安装或公证。构建/链接最低目标为 macOS 13.0，**实际验证平台是 macOS 27 arm64**；macOS 13–26、Intel 与官方 Claude 实机兼容未验证。

普通 `go build` 产物仅供隔离假环境开发。dyld 可在 Go main 之前处理环境，main 内过滤不能补救此前泄漏。不要从污染环境新起解释器来“清理”；使用可信已运行 zsh 或已有且已验证的硬化产物。恶意词法 alias、DEBUG/ZERR/PS4 注入、解释器/模块篡改或任意同 UID 攻击不属于 source 文件能提供的隔离。

## 可选 Skill 安装

工具本身可独立使用。若使用 Codex，可从**本仓库根目录**把整个仓库链接到用户 Skill 目录：

```zsh
(
  cc_health_skill_target="$HOME/.agents/skills/cc-anti-ban-skill" &&
  if [[ -e "$cc_health_skill_target" || -L "$cc_health_skill_target" ]]; then
    print -u2 -- 'Skill 目标已存在；未作修改。'
    exit 1
  fi &&
  mkdir -p "$HOME/.agents/skills" &&
  ln -s -h "$PWD" "$cc_health_skill_target"
)
```

入口会先拒绝已有文件、目录和符号链接（包括悬空链接），返回非零状态，不覆盖目标或在已有目录内创建嵌套链接。安装辅助变量也只存在于子 shell 中。检查与创建之间不要由其他进程同时改动安装目录；这不是并发置换的原子保护。

保留原仓库位置；不要只复制 `SKILL.md`，它依赖 `references/`、`scripts/`、`cmd/`、`internal/`、`go.mod` 等完整目录。其他代理按自己的安装说明保留完整仓库。安装 Skill 不授权真实体检、外联或启动 Claude；每次仍以用户的当前范围与确认卡为准。[Codex 官方目录说明](https://learn.chatgpt.com/docs/build-skills#where-to-save-skills)。

## 离线演示、验证与许可

直接用浏览器打开 [demo/index.html](demo/index.html) 可体验纯静态演示；83→95 是示例，不是本机检查结果。演示不连接检测服务，与实际报告状态分离。

![静态演示的初始示例状态](demo/preview.jpg)

[验证说明](references/validation.md)给出隔离假 HOME/CLI、回环、PTY 的复现方式和已验证/未验证范围；不会要求你在真实配置上重跑测试。项目没有 CI、自动发布或预编译发行包。

本项目采用 [MIT License](LICENSE)，版权标识为仓库所有者 GitHub handle `TIANTIAN-ZHAN`。依赖与素材范围见 [NOTICE](NOTICE.md)。Claude/Anthropic 等名称属于各自权利人；MIT 不授予第三方商标、SDK 或系统组件的权利。本工具不提供防封或恢复账户承诺。
