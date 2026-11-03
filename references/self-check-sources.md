# 自查来源与证据限度

[自查指南](self-check.md) · [Skill 入口](../SKILL.md)

本页只保留会改变自查决策的依据，不复制官方手册。来源性质与用户个案状态是两件事：官方文档说明规则，并不自动证明某个账号已满足规则。

## 官方依据

| 来源 | 自查时怎么用 |
| --- | --- |
| [Claude Code 网络配置](https://code.claude.com/docs/en/network-config) | 官方支持显式 HTTP(S) 代理环境变量，启动环境与客户端模式会影响配置来源；不能据此要求人人开启全局代理＋TUN。本工具支持范围更窄，仍按自己的拒绝条件处理认证代理、自定义信任等情形 |
| [Claude 是否使用位置](https://privacy.claude.com/en/articles/11186740-does-claude-use-my-location) | 官方说明会用 IP 和其他信号推断大致地区用于安全与服务资格；这不证明“每次换 IP 都会扣分”，也不能从第三方出口标签确认本人资格 |
| [Claude 可用地区](https://support.claude.com/en/articles/8461763-where-can-i-access-claude) | 用户按本人真实地区及所用产品核对当前官方范围；组织/API 等产品还需看各自适用条款，不用代理所在地替代 |
| [安装与认证排障](https://support.claude.com/en/articles/14552646-troubleshoot-claude-code-installation-and-authentication) / [API 错误](https://platform.claude.com/docs/en/api/errors) | 连接、认证、权限、额度等要分开处理；403 可能涉及模型或资源权限，不能只凭错误码宣布封号 |
| [身份验证说明](https://support.claude.com/en/articles/14328960-identity-verification-on-claude) | 按官方提示由本人处理身份验证；Agent 只需要类别和脱敏状态，不收证件或代判审核结果 |
| [警告与申诉说明](https://support.claude.com/en/articles/8241253-safeguards-warnings-and-appeals) | 明确停用时用官方复核入口准备材料；本项目不决定停用原因或恢复账号 |

官方页面可能更新；解释影响下一步的规则时以当前适用页面为准。不要把上述概括扩展成未被来源支持的账号风险公式。

## 社区帖子：线索，不是官方计分依据

参考 [make claude 的帖子](https://x.com/make_claude/status/2115501814090969583)（2026-10-28 17:51:51 UTC）及 [作者后续说明](https://x.com/make_claude/status/2115667744339484821)。主帖讨论隐蔽积分、观察期等机制，附有代码展示；**该社区代码展示的来源与机制尚未由本项目验证，不作为官方规则或账号计分依据**。这不等于认定代码造假，也不推断作者是否拥有未公开证据。

作者后续说明区分了正常同城/上下班换网与其关注的跨国跨度、频率。自查只把“故障前后是否有相关变化”作为可询问线索，不把任意 IP 变化写成危险，不据此推定封禁因果。

本包不采纳邮箱品牌、操作系统、App、开启记忆、Pro 升 Max 等待期等所谓加减分结论，不据此自动变更账号、订阅或设备。Canvas/WebGL 伪装是评论区其他人的推广，不能冒充原作者建议。多号轮换、遇 KYC 弃号、强制全局＋TUN 等也不是本包的自动处置路线。

## Net.Coffee：外部参考与分享封面

| 页面 | 可参考的用途 |
| --- | --- |
| [Claude AI IP 风险检测](https://ip.net.coffee/claude/) | 查看第三方对 Claude 使用环境信号的组织方式 |
| [IP / 网络信息](https://ip.net.coffee/ip/) | 了解第三方展示的 IP 与网络信息类别 |
| [Claude Code 页面](https://ip.net.coffee/claude/claudecode.html) | 参考其环境配置说明；具体配置以适用的官方文档为准 |

README 使用 [Claude 主题分享封面](https://ip.net.coffee/og-image-claude.png) 和 [通用分享封面](https://ip.net.coffee/og-image.png) 的原站 URL，第三项用文字预览。这两张图是原站静态分享卡，**不是检测报告截图、本工具 UI 或用户实测结果**；署名 Net.Coffee，权利边界见 [NOTICE](../NOTICE.md)。本项目未接入其评分服务。

打开检测页面可能自动发起第三方检测（内容依页面与浏览器而定）。只读本页、查看静态封面或准备自查材料，不授权打开检测页、嵌入 iframe、执行其脚本或抓取本机报告。用户主动给出的脱敏报告可以单列来源与时间，但其分数不能当作 Anthropic 账号结论，不能进入本工具数字评分。
