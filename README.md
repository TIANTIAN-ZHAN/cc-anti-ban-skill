<p align="center">
  <img src="docs/assets/readme-hero.svg" alt="Claude 封号如何破解？CC Anti-Ban｜Claude 封号急救包：自查清单、证据整理、申诉准备" width="100%">
</p>

<p align="center">
  <strong>CC Anti-Ban｜Claude 封号急救包</strong><br>
  自查清单、证据整理、申诉准备，一次备齐。
</p>

<p align="center">
  <a href="references/self-check.md">开始自查</a> ·
  <a href="#for-ai-agents">交给 Agent 安装</a> ·
  <a href="#参考页面示例">参考页面示例</a> ·
  <a href="#聊聊你的报错">交流反馈</a> ·
  <a href="README.en.md">English</a>
</p>

## 先分清问题，再准备下一步

连接失败、登录报错、额度或身份验证提示，还是已经收到明确停用通知？这些情况需要不同的处理路径。急救包把**按需自查、脱敏证据和申诉草稿**放在一起，附带一个可独立运行的 macOS Claude Code 环境工具。

| 你遇到的情况 | 从这里开始 |
| --- | --- |
| 连接、代理、安装或认证错误 | 查相关环境与入口，保留具体错误；单个 403 不足以判定封号 |
| 支付、KYC、订阅或用量提示 | 在官方页面由本人核对，Agent 帮你整理脱敏摘要 |
| 明确的账号停用通知 | 梳理时间线，准备 [官方申诉](https://support.claude.com/en/articles/8241253-safeguards-warnings-and-appeals) 材料 |
| 只有猜测或互相冲突的信息 | 标清哪些是自述、观察或未确认，只补影响下一步的缺项 |

这是自查与材料准备工具包，不是解除封禁的软件；账号判定和申诉处理由官方负责。

## 自查时，不靠“账号积分”猜结论

[自查指南](references/self-check.md)按相关性覆盖七类线索：**错误类型、客户端与认证、真实地区与资格、近期相关变化、当前启动代理、支付/KYC/订阅/用量、下一步选择**。不必逐项答题，也不要求为了安装先交代账号信息。

交付的是 Agent 文字报告：每项标明“用户自述 / 工具观察 / 未确认 / 不适用”，附来源和时间。用户主动给出的第三方脱敏报告单列记录，不充当官方账号结论，也不混入本工具数字评分。

清单区分官方依据、工具观察、用户自述和待核的社区经验，不把经验帖变成账号加减分规则，也不把正常同城换网本身当作封禁证据。具体出处与证据限度见 [官方依据与社区来源](references/self-check-sources.md)。

## For AI Agents

### 先把急救包装好

操作者是 **Codex、Kimi Code**，或使用 **DeepSeek 等模型**且有本地文件与终端工具的独立 Coding Agent。**Claude 是被检查对象。** 复制这段即可交给 Agent：

```text
帮我安装「CC Anti-Ban｜Claude 封号急救包」：
https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill

先读完整仓库中的 INSTALL.md 和 SKILL.md，检查依赖，
使用 scripts/build-safe.zsh 构建，只在本次构建成功后运行 --help。
保留完整仓库，不覆盖已有目录；缺权限或依赖就说明阻塞。
先安装独立工具，不自动安装到任何 Agent 的 Skill 目录。
安装期间不启动自查问卷、真实检查、serve、网络探测或 Claude，
不读取账号资料或修改系统设置。
完成后报告仓库/二进制路径与构建、help 结果，等我选择下一步。
```

**[INSTALL.md → 手动安装与可选 Codex Skill](INSTALL.md)**

### 已安装，开始文字自查

把脱敏错误和已有信息交给 Agent，例如：

> 使用这个仓库的自查指南梳理我这次的问题。先用我已经提供的信息，只问相关缺项，给出证据状态和下一步。先做文字整理，不自动访问本机、检测网站或提交申诉。

Skill 会按请求选择安装、自查、报告解释或申诉准备。没有明确发送/提交授权时，只交草稿，不联系 Support；支付和身份验证由本人在官方页面处理。各 Agent 客户端完整兼容性尚未做端到端验收。

## 参考页面示例

前两张图来自用户提供的**同一张 Net.Coffee 页面截图，已脱敏**；第二张为详情裁剪。图中数值保留原图显示，未由本项目复测，不代表本工具效果或 Anthropic 账号判定。第三项用文字预览。感谢 Net.Coffee 提供界面与信息组织参考，本项目未接入其评分服务。

### 01 · Claude AI IP 风险检测

<a href="https://ip.net.coffee/claude/"><img src="docs/assets/netcoffee-user-screenshot-redacted.png" alt="用户提供的 Net.Coffee Claude 页面截图，IP 等识别信息已脱敏" width="600"></a>

用户提供截图（已脱敏）；保留原截图范围，原图未包含完整网页。

[打开 Claude 检测参考 →](https://ip.net.coffee/claude/) · 第三方页面，打开可能自动检测。

参考它如何组织 Claude 使用环境的信号；分数不代表 Anthropic 的账号判定。

### 02 · 同图详情：IP 属性与安全检测

<a href="https://ip.net.coffee/claude/"><img src="docs/assets/netcoffee-user-screenshot-detail.png" alt="同一张 Net.Coffee 脱敏截图中 IP 属性与安全检测两卡的详情裁剪" width="600"></a>

同一截图下方两卡的裁剪，不是另一次检测。[截图来源 →](https://ip.net.coffee/claude/) · [IP 信息参考（另页）→](https://ip.net.coffee/ip/)；打开第三方页面可能自动检测。

用于了解第三方展示的网络信息类别；不能用出口标签替代本人真实地区与使用资格。

### 03 · Claude Code 环境说明

> **[Net.Coffee · Claude Code 参考页面 →](https://ip.net.coffee/claude/claudecode.html)**
>
> 第三方话题预览：IP、DNS、IPv6、分流等 Claude Code 使用环境说明，可与前两项对照阅读；具体配置以 [Claude Code 官方文档](https://code.claude.com/docs/en/network-config) 为准。

第三方站点可能自动采集 IP/DNS/WebRTC/设备信息。查看本地脱敏图与阅读指南不等于授权运行检测；由用户自行决定是否打开原站。仅保存用户授权的脱敏衍生图，权利与来源见 [NOTICE](NOTICE.md)。

## 附带的本地工具能做什么

现有 `cc-health` 继续检查 macOS 上 Claude Code 的安装、基础配置与启动代理；经单次确认可执行限定参考链路探测，或为受支持的新会话应用回环 HTTP 代理。默认不主动外联，不修改系统代理、原 shell 或配置文件。

参考评分与覆盖率来自现有工具；账号字段始终未知、零通过权重。文字自查不会改写 CLI 报告。范围见 [报告契约](references/report-contract.md)，安装步骤见 [INSTALL.md](INSTALL.md)。

## 使用范围

- 本地工具需要符合[安装说明安全门槛](INSTALL.md#1-准备环境)的稳定版 Go、clang/SDK 与可信已运行 zsh；实际验证为 macOS 27 arm64，旧系统、Intel 及官方 Claude 完整兼容性未实证，当前无预编译包。
- 本地检查可能读取含凭据的环境与配置原文并进入内存；原文不外发、写日志或导出，不承诺立即擦除。[隐私与授权范围](references/safety-and-privacy.md)
- 不提供解封、防封或破解官方风控的保证；不收账号凭据、证件、支付资料或完整原始配置，不代替官方验证。

## 聊聊你的报错

[到 Issues 交流或反馈](https://github.com/TIANTIAN-ZHAN/cc-anti-ban-skill/issues)：分享脱敏现象、复现步骤，以及哪些信息已观察、哪些仍未知。不要上传账号、邮箱、token、证件、原始配置或含凭据的代理/订阅链接。

[自查指南](references/self-check.md) · [来源说明](references/self-check-sources.md) · [安装](INSTALL.md) · [隔离验证](references/validation.md) · [English](README.en.md)

[早期静态原型](demo/index.html)中的 83→95、地区/IP/WebRTC 是虚构交互示例，不是当前实现或实测成果。项目独立于 Anthropic，采用 [MIT License](LICENSE)；第三方名称与素材不因本仓库许可而转为自有，详见 [NOTICE](NOTICE.md)。
