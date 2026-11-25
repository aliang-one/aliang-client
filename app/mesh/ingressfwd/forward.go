// Package ingressfwd 解决双进程拓扑下的 SSH 入口进程劈叉:mesh 中继 accept
// 与打洞流喂入都发生在 agent 进程(平台 WS 与 tunnel 归属),而 SSH ingress
// serve(含 mesh-exit 注入)按设计归 core 进程(数据面住 core,cmd/core.go
// 启动序列 reconcile)。sshin 单例是进程级的——agent 进程里恒缺席,若只按
// 原契约丢弃,双进程设备(如 liang-dev 双单元部署)的 B 侧中继结构性不可用。
//
// 契约:本进程单例在跑 → 原路径 serve(返回 true);缺席 → 把「SSH 族」原始
// 连接 splice 到 core 统一入口/专用口,字节级搬运,core 侧门禁(forward gate/
// form 矩阵)照常执法;非 SSH 流与无处可转 → 关闭并返回 false。
//
// 只搬 SSH(2026-11-24 审计):中继/打洞通道是给 mesh 对端拨入用的(SSH 族)。
// 拼接是回环拨号——core 的统一入口按 TCP 对端地址判代理面来源(inbound/http
// 来源闸),对回环拨号恒放行;把 HTTP/socks5 字节拼过去,等于让远端以「回环
// 来源」身份使用代理面。首段不是 SSH 版本串即关,不拨 core。
package ingressfwd

import (
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"aliang.one/nursorgate/common/logger"
	sniff "aliang.one/nursorgate/inbound/sniff"
	"aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// spliceBuf 与 app/tunnel bridge 的双向拷贝同规格。
const spliceBuf = 32 * 1024

// coreDialTimeout 拨 core 入口的单目标回环拨号上限。core 与 agent 同机,
// 正常毫秒级;给满 5s 容忍负载毛刺,失败即试下一个目标/按不可达丢弃。
const coreDialTimeout = 5 * time.Second

// Target 返回 core 侧 SSH ingress 的本机回环拨号地址(首选目标,见 Targets)。
//
// 跟随形态(专用口未写 / 0 / 旧默认哨兵 56442)一律拨统一口 56432——SSH 是
// 统一口嗅探的三族之一,core 自己会按入口开关执法(未启用即拒收断连)。
// 这里刻意不以 agent 进程的配置副本判「入口是否启用」:双进程拓扑下两份
// 配置可能不同文件(macOS:core 读系统数据目录,agent 读用户目录),副本
// 没有 ssh_ingress 时旧实现返回空串直接关流——而 core 的入口其实开着。
//
// 2026-11-24 审计实证的缺陷:EffectiveSSHIngress() 早已把 port 0 归一成哨兵
// 56442(types.go,2026-11-10 进主线),而旧 Target 以「!= 0」判「是否配了专用
// 口」——默认配置下把全部中继/打洞流拼到 127.0.0.1:56442 这个 core 从不监听
// 的口。哨兵翻译的唯一口径是 processor/config 的 SSHIngressDefaultPort。
func Target(cfg *config.Config) string {
	return Targets(cfg)[0]
}

// Targets 返回 core 侧入口的回环拨号地址,按尝试顺序:
// 显式专用口(ServiceBindHost:port 常驻监听,不随代理启停)在前,
// 统一口 56432 在后(随代理运行);跟随形态只有统一口一个目标。
func Targets(cfg *config.Config) []string {
	unified := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(config.DefaultHTTPProxyPort)))
	if cfg == nil {
		return []string{unified}
	}
	eff := cfg.EffectiveSSHIngress()
	if eff == nil {
		return []string{unified}
	}
	if eff.Port == 0 || eff.Port == config.SSHIngressDefaultPort {
		return []string{unified}
	}
	return []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(int(eff.Port))), unified}
}

// ServeOrForward 本进程单例在跑 → serve(阻塞至会话结束,返回 true);
// 缺席 → 仅 SSH 族转发到 core(阻塞至流结束)并返回 false;非 SSH 流 →
// 关闭并返回 false。调用方对 false 统一 WARN 记账(与原 drop 路径同位)。
func ServeOrForward(c net.Conn, cfg *config.Config) bool {
	if c == nil {
		return true
	}
	if ssh.IsRunning() {
		ssh.HandleIngressConn(c)
		return true
	}

	// 只搬 SSH:嗅探首段(回放缓存无损交给 splice),非 SSH 即关。
	kind, stream, err := sniff.Sniff(c)
	if err != nil || kind != sniff.KindSSH {
		logger.Info(fmt.Sprintf("[MESH-RELAY-FWD] dropped non-SSH stream from %s (kind=%s err=%v)",
			safeRemoteAddr(c), kind, err))
		_ = c.Close()
		return false
	}

	upstream, target, dialErr := dialFirstReachable(Targets(cfg), coreDialTimeout)
	if dialErr != nil {
		logger.Info(fmt.Sprintf("[MESH-RELAY-FWD] local ingress absent, forwarding %s -> core failed: %v",
			safeRemoteAddr(c), dialErr))
		_ = c.Close()
		return false
	}
	logger.Info(fmt.Sprintf("[MESH-RELAY-FWD] local ingress absent, forwarding %s -> core %s",
		safeRemoteAddr(c), target))
	if err := splice(stream, upstream); err != nil {
		logger.Info(fmt.Sprintf("[MESH-RELAY-FWD] forward %s ended: %v", safeRemoteAddr(c), err))
	}
	return false
}

// Splice 把 c 的字节流双向搬运到 addr(拨号 5s 上限),任一向结束即双向
// 收尾。SSH 流对字节保序敏感,用 io.Copy 直拷,零解析零改写。
func Splice(c net.Conn, addr string) error {
	upstream, err := net.DialTimeout("tcp", addr, coreDialTimeout)
	if err != nil {
		_ = c.Close()
		return fmt.Errorf("dial core ingress %s: %w", addr, err)
	}
	return splice(c, upstream)
}

// dialFirstReachable 按序拨目标,返回第一个连通的连接与其地址;全部失败
// 返回最后一个错误(含目标清单,便于排障)。
func dialFirstReachable(targets []string, timeout time.Duration) (net.Conn, string, error) {
	var lastErr error
	for _, target := range targets {
		conn, err := net.DialTimeout("tcp", target, timeout)
		if err == nil {
			return conn, target, nil
		}
		lastErr = fmt.Errorf("dial core ingress %s: %w", target, err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no core ingress target configured")
	}
	return nil, "", lastErr
}

// splice 把 c 与 upstream 双向搬运,任一向结束即双向收尾。
func splice(c net.Conn, upstream net.Conn) error {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, c)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, upstream)
		done <- struct{}{}
	}()
	<-done
	// 任一向结束(EOF/错误)即双向收尾:SSH 会话一旦半断即废,半关闭
	// 语义无收益,显式双关防对端悬挂。
	_ = upstream.Close()
	_ = c.Close()
	<-done
	return nil
}

// safeRemoteAddr 日志用;remote label 包裹态返回标签,兜底 hex 编码防注入。
func safeRemoteAddr(c net.Conn) string {
	addr := c.RemoteAddr()
	if addr == nil {
		return "?"
	}
	s := addr.String()
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return hex.EncodeToString([]byte(s))
		}
	}
	return s
}
