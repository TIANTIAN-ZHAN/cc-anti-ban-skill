package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cc-anti-ban-skill/internal/execution"
	"cc-anti-ban-skill/internal/health"
	"cc-anti-ban-skill/internal/localweb"
	"cc-anti-ban-skill/internal/runner"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, "cc-health — macOS + Claude Code 环境证据与新会话代理\n\n  cc-health serve                 启动本机界面；活动授权 15 分钟，残留时仅保留清理；操作均需页面内确认\n  cc-health check --local-consent  同意以下本地只读检查并输出脱敏 JSON（不外联）\n\n本地范围：PATH 查找、禁网禁写沙箱内 claude --version、完整进程环境枚举、固定用户 settings.json 有界全文读取及常见配置元数据。\n环境/配置可能含 API key、代理密码等敏感值，会在读取与解析时进入本地内存；本次会话保留摘要和必要上下文，不把原文发给外部服务、写入日志或导出。\n不主动打开 Keychain、浏览器 Cookie、聊天或独立凭据文件；DYLD 按名称识别，不保留或输出其值。CLI 绝对路径与工作目录只在新会话确认卡显示供核对，不进入日志或导出。默认没有任何探测请求。\n外联及新会话代理仅在界面审阅具体计划后单次确认；正常 Claude 启动有自身网络/写入/hooks，不受参考探测预算保证。不验证账户/额度/模型；不修改系统或原 shell。")
		return 0
	}
	if len(args) == 2 && args[0] == "check" && args[1] == "--local-consent" {
		s := health.Inspect(context.Background(), health.SystemIO())
		if json.NewEncoder(out).Encode(s.Report) != nil {
			return 1
		}
		return 0
	}
	if len(args) != 1 || args[0] != "serve" {
		fmt.Fprintln(errOut, "参数无效；运行 cc-health --help 查看范围与用法。")
		return 2
	}
	return serve(out, errOut)
}

func serve(out, errOut io.Writer) int {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(errOut, "无法启动本机服务。")
		return 1
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expiry := time.AfterFunc(15*time.Minute, cancel)
	defer expiry.Stop()
	terminal := runner.New(os.Stdin, os.Stdout, os.Stderr)
	manager := execution.New(ctx, execution.Dependencies{Recheck: func(ctx context.Context, previous health.Snapshot) health.Snapshot {
		return health.Reinspect(ctx, health.SystemIO(), previous)
	}, Available: terminal.Available, Start: func(prepared *runner.Prepared, env []string) (execution.Process, error) {
		return terminal.Start(prepared, env)
	}, Environment: os.Environ, Network: health.SystemNetwork, Now: time.Now})
	defer manager.Close()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		if serviceStopSignal(ctx, signals, terminal.OwnsInterrupts) != nil {
			cancel()
		}
	}()
	bootstrap := localweb.Token()
	actions := localweb.Actions{Scan: func(ctx context.Context) health.Snapshot { return health.Inspect(ctx, health.SystemIO()) }, Plan: func(s health.Snapshot, now time.Time) health.Plan { return s.Plan(now) }, Probe: func(ctx context.Context, s health.Snapshot, p health.Plan) health.Report {
		return s.Probe(ctx, p, health.SystemNetwork())
	}, Close: cancel, Now: time.Now, Execution: manager, ActivityContext: ctx}
	handler := localweb.New(listener.Addr().String(), bootstrap, actions)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return context.Background() }}
	fmt.Fprintln(out, "本机界面已启动；尚未执行环境检查。链接含一次性会话凭据，请只在本机打开。活动授权 15 分钟后到期；残留资源清理期间仅保留本地状态与重试；页面结束会停止本次拥有的根进程。启动过交互会话后 Ctrl-C 保留给终端交互，请用页面结束服务。")
	fmt.Fprintf(out, "http://%s/#%s\n", listener.Addr().String(), bootstrap)
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		handler.Expire()
		if manager.ResourcesPending() {
			fmt.Fprintln(errOut, "活动授权已结束；正在停止根进程。若临时副本仍保留，本机会话仅提供状态和清理重试，清理成功后自动退出。")
		}
		for manager.ResourcesPending() {
			time.Sleep(100 * time.Millisecond)
		}
		shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Close()
	}()
	err = server.Serve(listener)
	cancel()
	<-done
	manager.Close()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	stopped := manager.Wait(waitCtx)
	waitCancel()
	if !stopped {
		fmt.Fprintln(errOut, "本机服务已关闭，但本次进程退出尚未确认；请检查原终端。没有声称回滚副作用或清空后代。")
		return 1
	}
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(errOut, "本机服务已停止。")
		return 1
	}
	return 0
}

func serviceStopSignal(ctx context.Context, signals <-chan os.Signal, ownsInteractiveHistory func() bool) os.Signal {
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig := <-signals:
			if sig == os.Interrupt && ownsInteractiveHistory() {
				continue
			}
			return sig
		}
	}
}
