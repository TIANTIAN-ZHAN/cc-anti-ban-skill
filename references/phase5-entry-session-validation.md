# 验证专题：构建分派与会话到期

这是已有隔离验收主题的公开摘要，不是用户真实 Claude/账号/网络验收记录。原始个人机器路径、内部会话标识和外部工作目录不属于公开复现依赖。

覆盖 builtin/command 函数遮蔽、特殊表影射、readonly 和失败控制流；过期 bootstrap 被撤销，既有清理 session 保留，接受与 Expire 有明确先后。

可信解释器与词法解析是前提，恶意 alias/trap/PS4 可能在门禁前执行。失败时不得继续启动旧二进制。

复现从 [统一验证说明](validation.md)开始，使用 [对应源码测试](../internal/localweb/session_expiry_test.go)。当前代码与测试是事实来源；不要把历史测试结果用于推定未测试的平台、真实账户或服务可用性。
