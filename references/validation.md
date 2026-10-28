# 隔离验证与边界

已验证的是实现行为，不是用户账号或真实 Claude 服务可用性。历史验收使用 macOS 27 arm64、Go 1.25.6、本地 clang/SDK；其他系统/架构及官方 CLI 完整兼容未实证。构建的 minos13 只是最低部署目标。

## 可复现测试

在工具仓库根目录和可信、没有恶意注入的已运行 shell 中执行。所有子程序使用假 HOME/CLI、任务临时目录或独立 PTY；实际请求只到回环 fixture，不连接真实代理/API。不要将真实配置复制进测试目录。

```zsh
cc_check_root="$(mktemp -d "${TMPDIR:-/tmp}/cc-health-check.XXXXXX")" &&
mkdir -p "$cc_check_root/home" "$cc_check_root/tmp" "$cc_check_root/cache" "$cc_check_root/gopath" &&
env -i PATH="$PATH" HOME="$cc_check_root/home" TMPDIR="$cc_check_root/tmp" \
  GOCACHE="$cc_check_root/cache" GOPATH="$cc_check_root/gopath" \
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=1 \
  MACOSX_DEPLOYMENT_TARGET=13.0 CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0" \
  CGO_LDFLAGS="-mmacosx-version-min=13.0" \
  go test -race -count=1 ./...
```

在同一隔离环境将最后的 `go test -race -count=1 ./...` 换为 `go vet ./...` 可做静态检查。Go 模块仅标准库，模块代理/校验服务和工具链下载被禁用。测试产物/缓存由你指定的临时目录保留，切勿将真实报告作为测试输入或提交。

可选 UI fixture 默认跳过；它们需要明确设置测试 URL 文件变量且文件必须位于测试 TMPDIR 下，最长运行 3 分钟。它们模拟用户流程；普通测试不会调用 SystemIO/SystemNetwork 做实机检查。测试实现中的变量与场景以相应 `*_test.go` 为准。

## 覆盖范围

| 主题 | 复现入口 |
| --- | --- |
| 本地读取边界、凭据不进入报告、摘要绑定、评分与特殊地址 | [health 测试](../internal/health/health_test.go)、[恢复边界](../internal/health/recovery_test.go) |
| 单次清单、回环 TLS/CONNECT/HEAD、取消后不进入下一阶段 | [probe 测试](../internal/health/probe_test.go)、[活动取消](../internal/localweb/activity_test.go) |
| 批准对象固定、唯一 reaper/根 PID 信号、路径置换及真实假 PTY | [runner 测试](../internal/runner/process_darwin_test.go)、[监督](../internal/runner/supervisor_test.go) |
| Mach-O raw 命令/范围拒绝、清理 FD/最小删除/还原受阻 | [Mach-O](../internal/runner/macho_test.go)、[清理对象](../internal/runner/artifact_test.go) |
| manager/API/UI 的 retained 重试、权限与到期 session | [清理 HTTP](../internal/localweb/cleanup_darwin_test.go)、[会话到期](../internal/localweb/session_expiry_test.go) |
| 本次构建失败或目录输入无效不启动、成功后正确项目 cwd、父 shell 状态保留、Skill 安装拒绝已有目标 | [README 命令测试](../cmd/cc-health/readme_darwin_test.go) |

## 如何解释结果

保留测试失败与后续修复的区别；测试通过不能代替真实设备授权。参考阶段未执行时尝试数为零、开始时间为空；失败/过期/取消不称完成或可比。真实 HTTP 状态不推断账户状态。

此前主题记录已整理为无身份的专题说明：[基础检查](phase1-validation.md)、[新会话](phase2-validation.md)、[对象与监督](phase2-reviewfix-validation.md)、[加载器边界](phase3-hardening-cleanup-validation.md)、[生命周期](phase4-lifecycle-validation.md)、[构建/会话入口](phase5-entry-session-validation.md)、[隐私告知](phase6-disclosure-validation.md)。它们不包含无法对外访问的原始机器日志链接；当前可执行证据以本仓库测试和自己的隔离回执为准。

## 未作保证

未验收真实 Claude 账号、订阅、额度、模型、真实代理或组织策略。没有任意同 UID 权限隔离、完整进程树回滚、内存立即擦除、跨崩溃恢复、正式 Developer ID/公证、跨系统发行保证。恶意解释器/词法 alias/trap/PS4 在脚本门禁之前的执行不能由 source 文件补救。
