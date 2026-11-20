// Package ingressfwd 解决双进程拓扑下的 SSH 入口进程劈叉:mesh 中继 accept
// 与打洞流喂入都发生在 agent 进程(平台 WS 与 tunnel 归属),而 SSH ingress
// serve(含 mesh-exit 注入)按设计归 core 进程(数据面住 core,cmd/core.go
// 启动序列 reconcile)。sshin 单例是进程级的——agent 进程里恒缺席,若只按
// 原契约丢弃,双进程设备(如 liang-dev 双单元部署)的 B 侧中继结构性不可用。
//
// 契约:本进程单例在跑 → 原路径 serve(返回 true);缺席且 core 侧入口已
// 启用(同一份 customer 配置,双进程共享同一 HOME)→ 把原始连接 splice 到
// core 统一入口/专用口,字节级搬运,core 侧门禁(forward gate/form 矩阵)
// 照常执法;缺席且入口未启用 → 关闭并返回 false,调用方 WARN 记账。
package ingressfwd

import (
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// spliceBuf 与 app/tunnel bridge 的双向拷贝同规格。
const spliceBuf = 32 * 1024

// coreDialTimeout 拨 core 统一入口的本机回环拨号上限。core 与 agent 同机,
// 正常毫秒级;给满 5s 容忍负载毛刺,失败即按不可达丢弃(调用方日志)。
const coreDialTimeout = 5 * time.Second

// Target 返回 core 侧 SSH ingress 的本机回环拨号地址;入口未启用返回空串
// (无处可转)。跟随形态(专用口未配)拨统一口 56432——SSH 是统一口嗅探
// 四族之一;专用口形态拨 core 侧专用 listener。双进程共享同一 customer
// 配置(同 HOME 同文件),agent 侧据此推 core 的运行面。
func Target(cfg *config.Config) string {
	eff := cfg.EffectiveSSHIngress()
	if eff == nil {
		return ""
	}
	port := int(config.DefaultHTTPProxyPort)
	if eff.Port != 0 {
		port = int(eff.Port)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// ServeOrForward 本进程单例在跑 → serve(阻塞至会话结束,返回 true);
// 缺席 → 转发或关闭(返回 false,调用方 WARN 记账,与原 drop 路径同位)。
func ServeOrForward(c net.Conn, cfg *config.Config) bool {
	if c == nil {
		return true
	}
	if ssh.IsRunning() {
		ssh.HandleIngressConn(c)
		return true
	}
	target := Target(cfg)
	if target == "" {
		_ = c.Close()
		return false
	}
	logger.Info(fmt.Sprintf("[MESH-RELAY-FWD] local ingress absent, forwarding %s -> core %s",
		safeRemoteAddr(c), target))
	if err := Splice(c, target); err != nil {
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
