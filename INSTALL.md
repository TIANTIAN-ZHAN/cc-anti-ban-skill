# 安装 CC Anti-Ban

[产品介绍](README.md) · [English](README.en.md) · [Agent 操作边界](SKILL.md)

这是人工和 Coding Agent 共用的权威安装说明。仓库与 Skill 名称均为 `cc-anti-ban-skill`，命令为 `cc-health`。**安装到本次构建成功并通过 `--help` 为止；真实检查、外联和启动 Claude 是另外的操作。**

## For AI Agents

操作者可以是 Codex、Kimi Code，或使用 DeepSeek 等模型且具备本地文件与终端工具的其他 Coding Agent。模型名称本身不代表文件/终端能力。Claude Code 是被检查对象，请由独立 Agent 完成安装与诊断；不把本项目安装到 `.claude/skills`，也不依赖 Claude 自检。

收到安装请求后，依次完成下列工作：

1. 阅读本文及 [SKILL.md](SKILL.md)，确认系统、依赖、可信 shell 和用户指定的仓库位置。复用已有完整仓库时核对来源与改动，不覆盖或重置它。
2. 获取完整仓库；遇到访问拒绝，说明权限阻塞并停在该步。让用户通过自己的 Git 客户端处理权限，不索要或在命令/聊天中粘贴明文 token。
3. 执行下方“只构建并验收”。仅运行本次成功构建的 `--help`，不能用旧产物掩盖构建失败，也不要执行 `serve` 或 `check` 来证明安装成功。
4. 默认交付独立工具。只有用户选择可选 Codex Skill 安装时，才执行对应链接步骤；Kimi 和其他 Agent 可直接读取本仓库的 `SKILL.md` 与本文工作，不需要先安装 Skill。
5. 报告仓库位置、构建结果、二进制绝对路径、`--help` 退出状态，以及可选 Skill 是否安装。说明真实检查尚未执行，等待用户选择检查项目并授权。

安装请求不授权读取真实 Claude 配置、运行 Claude、探测代理/API、调用模型或更改系统代理。缺依赖时列出缺项和安装方法，由用户选择是否安装；不要自动下载安装器或改变全局配置。

## 1. 准备环境

| 依赖 | 要求 |
| --- | --- |
| 系统 | macOS；实际验证为 macOS 27 arm64，构建最低目标为 13.0 |
| 源码 | Git 与完整仓库；不能只下载 `SKILL.md` |
| 工具链 | Go 稳定版 1.26.10+（1.26 分支）、1.27.3+（1.27 分支）或更新稳定分支；Xcode 或 Command Line Tools 的 clang/SDK，以及系统 codesign |
| Shell | 已运行、解析与模块可信的 zsh |

无需 Node、Python、Claude 登录或 API key 来构建和打开工具界面。当前没有预编译发行包；Windows、Linux、Intel 和 macOS 13–26 未经本项目实机验证。

从可信环境确认依赖已安装。若发现 DYLD 污染或 shell 注入嫌疑，不要新起解释器来“清理”，先停止；[安全构建边界](references/phase5-entry-session-validation.md)解释了本入口能处理和不能处理的情况。构建脚本会禁用 Go 模块/工具链下载，本地工具链不满足要求时直接失败。

上述版本是 **2026-11-03 核定的最低修复版本**，来自 [Go 官方安全发行记录](https://go.dev/doc/devel/release)；推荐使用当前受支持分支的最新稳定补丁。`go.mod` 的 `go 1.25.0` 仅保留语言/API 最低要求，不是安装构建的安全门槛。入口拒绝旧补丁、beta/rc/devel 和不明版本，在版本查询与构建时均关闭自动工具链切换、忽略持久 GOENV/GOFLAGS 和外部 Go workspace；不会替你升级全局 Go。门槛不会自动跟踪未来漏洞或支持期限，版本较新也不是永无漏洞的保证。

在你选择的父目录获取源码。目标目录已存在时，先核实是否为要复用的仓库，不覆盖它：

```zsh
git clone https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill.git &&
cd -- cc-anti-ban-skill
```

以下代码块均从**完整工具仓库根目录**运行。仓库路径和用户项目路径可以包含空格。

## 2. 只构建并验收

此步骤只生成本地二进制并显示帮助，不读取 Claude 配置、不启动界面或执行检查。

<!-- command: build-only -->
```zsh
(
  cc_health_repo="$PWD" &&
  source "$cc_health_repo/scripts/build-safe.zsh" "$cc_health_repo/.build/cc-health" &&
  "$cc_health_repo/.build/cc-health" --help
)
```

整段成功退出即完成工具安装。构建失败则停止，不能直接运行 `.build/cc-health` 中可能遗留的旧文件。入口在子 shell 内执行，父 shell 的变量与目录不变。构建使用本地 ad-hoc hardened runtime 签名，不是 Developer ID 签名或公证。

## 3. 可选：安装为 Codex Skill

独立工具无需此步骤。[Codex 官方文档](https://learn.chatgpt.com/docs/build-skills#where-codex-loads-local-skills)说明用户 Skill 位于 `~/.agents/skills`，支持符号链接目录。

用户选择安装后，从仓库根目录执行整段。先拒绝已有目标，再重新构建并通过 `--help`，最后链接**整个仓库**：

<!-- command: codex-skill -->
```zsh
(
  cc_health_repo="$PWD" &&
  cc_health_skill_target="$HOME/.agents/skills/cc-anti-ban-skill" &&
  if [[ -e "$cc_health_skill_target" || -L "$cc_health_skill_target" ]]; then
    print -u2 -- 'Skill 目标已存在；未作修改。'
    exit 1
  fi &&
  if [[ -e "$HOME/.agents/skills" && ! -d "$HOME/.agents/skills" ]]; then
    print -u2 -- 'Skill 父路径不是目录；未作修改。'
    exit 1
  fi &&
  source "$cc_health_repo/scripts/build-safe.zsh" "$cc_health_repo/.build/cc-health" &&
  "$cc_health_repo/.build/cc-health" --help &&
  mkdir -p "$HOME/.agents/skills" &&
  ln -s -h "$cc_health_repo" "$cc_health_skill_target"
)
```

已有文件、目录、有效或悬空链接及重复安装均返回失败；不覆盖目标、不在已有目录内创建嵌套链接。检查与创建期间不要由其他进程同时修改安装目录，这不是并发置换的原子保护。保留原仓库位置及完整的 `references/`、`scripts/`、`cmd/`、`internal/`、`go.mod`；移动仓库会使链接失效。

[Kimi Code 官方 Skill 文档](https://www.kimi.com/code/docs/en/kimi-code-cli/customization/skills.html)列出了 `.agents/skills` 和 `.kimi-code/skills` 等扫描目录，但未保证上述单目录符号链接方式。这里不提供未经验证的 Kimi 一键安装命令；直接让 Kimi 读取完整仓库的本文与 `SKILL.md` 即可。Codex/Kimi 的实际客户端加载与完整诊断流程尚未做端到端兼容验收。

## 4. 用户授权后开始检查

这一步不属于安装验收。先让用户选择要检查的项目，并阅读下方本地数据范围。确认后，从工具仓库根目录运行：

<!-- command: start-ui -->
```zsh
(
  cc_health_repo="$PWD" &&
  source "$cc_health_repo/scripts/build-safe.zsh" "$cc_health_repo/.build/cc-health" &&
  read -r "cc_health_project?要检查的项目目录（绝对路径，可含空格）: " &&
  [[ "$cc_health_project" == /* && -d "$cc_health_project" ]] &&
  cd -- "$cc_health_project" &&
  "$cc_health_repo/.build/cc-health" serve
)
```

提示出现后输入已有项目的绝对路径，可含空格，不额外包引号。空、相对或不存在的路径不会启动。**`serve` 的当前目录就是被检查项目及后续新 Claude 的工作目录**；不要误用工具仓库作为目标项目。

打开终端给出的完整一次性本机链接，在页面内同意后才开始本地检查。链接含临时凭据，请勿分享。服务仅监听 `127.0.0.1` 随机端口；活动授权最多 15 分钟，刷新页面不会恢复授权。结束时使用页面的“结束本机服务”；如显示临时副本待清理，保留原页面完成重试。

只需 JSON 报告且用户已同意本地读取范围时，在上述整段入口中将最后的 `serve` 改为 `check --local-consent`。不要在安装期间使用它；它会立即检查，并且不执行主动探测。

### 检查前必须知道

本地检查会枚举完整进程环境，并有界读取固定用户 `settings.json` 原文；其中可能含凭据，会进入本地内存。程序保留必要摘要和上下文，不将原文发往外部服务、写日志或导出，不承诺立即擦除内存。安装/版本检查会在禁网禁写沙箱中运行 `claude --version`；不主动打开 Keychain、浏览器 Cookie、聊天或独立凭据文件。详见 [隐私与授权边界](references/safety-and-privacy.md)。

网络参考探测和新 Claude 会话都有独立确认卡。参考探测只到固定 Anthropic API 入口，不调用模型；正常启动的 Claude 则可自行联网、写文件、运行 hooks/MCP。新会话代理仅支持用户选择的回环 HTTP 代理和受支持的原生 Mach-O CLI，脚本/npm shim 等拒绝；官方 Claude 的完整兼容性未实证。分数不证明账号、地区资格、额度或模型可用性。

## 遇到安装问题

| 现象 | 下一步 |
| --- | --- |
| Git 拒绝访问 | 在自己的 Git 客户端完成权限配置；不要提交明文 token |
| 缺 Go、clang 或 SDK | 安装对应本地工具链后重试；不自动下载或降级构建 |
| 构建或签名失败 | 保留错误，停止；不要启动旧二进制或改用普通 `go build` 作为用户安装 |
| Skill 目标已存在 | 核对现有目标，由用户决定保留、迁移或删除；不强制覆盖 |
| Agent 没有文件/终端工具 | 在有这些能力的本地 Coding Agent 中打开仓库，或按本文手动操作 |

开发者验证见 [隔离验证与边界](references/validation.md)。普通 `go build` 产物仅用于隔离假环境开发；本安装路径使用 `scripts/build-safe.zsh`。
