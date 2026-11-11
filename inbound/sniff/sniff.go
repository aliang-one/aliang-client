// Package sniff 实现统一入口协议嗅探(spec §5):读前 4 字节把连接分派到
// SSH/socks5/HTTP 三协议族,已读字节经回放缓存无损交给下游。本包不依赖任何
// 协议包、不做门禁——分类器只分类,准入归各协议处理器。
package sniff

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

// Kind 是嗅探出的协议族。粒度刻意停在线格式最外层:完整解析(socks 版本
// 协商、HTTP 请求头)全部归各协议处理器,嗅探层只回答「该交给谁」。
type Kind int

const (
	// KindUnknown 表示首段缺失(超时或对端立即断开),调用方应断连。
	KindUnknown Kind = iota
	// KindSSH 是 SSH 版本串("SSH-" 前缀),含 SSH 入口双形态。
	KindSSH
	// KindSocks 是 socks5 greeting(VER=0x05,第二字节为方法数)。
	KindSocks
	// KindHTTP 是兜底:HTTP 请求以及一切未知字节流(与合并前「非 HTTP 即
	// ReadRequest 失败断连」等价,不劣化)。
	KindHTTP
)

func (k Kind) String() string {
	switch k {
	case KindSSH:
		return "ssh"
	case KindSocks:
		return "socks"
	case KindHTTP:
		return "http"
	default:
		return "unknown"
	}
}

// sniffTimeout 是首段读窗口。客户端已建立 TCP 却迟迟不发首段时不能无限占用
// 连接(分流点在最前面,一个静默连接会白占一个 goroutine)。var 而非 const
// 仅为测试注入短超时;默认值即生产值,取值参考 inbound/http 首包等待习惯。
var sniffTimeout = 2 * time.Second

// sniffPeekBytes 是分类所需的字节数。SSH 版本串需 4 字节,其余规则最多 2 字节。
const sniffPeekBytes = 4

// Sniff 分类一条已 accept 连接的协议族。
//
// 返回的 conn 后续 Read 先消费嗅探期已缓冲的字节、再落到底层连接,对下游
// 完全透明(与 inbound/http replayConn、processor/tcp WrappedConn 同款回放
// 语义,此处收口为一份实现)。调用方负责关闭返回的 conn。
//
// 首段缺失时返回 (KindUnknown, nil, err),调用方直接断连即可。
func Sniff(conn net.Conn) (Kind, net.Conn, error) {
	if conn == nil {
		return KindUnknown, nil, fmt.Errorf("sniff: nil conn")
	}
	// deadline 只覆盖首段读取,成功读到即清除:不污染后续会话流量(先设后清,
	// inbound/ssh 握手 deadline、出口侧 chain.go 同款先例)。
	_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
	reader := bufio.NewReader(conn)
	head, err := reader.Peek(sniffPeekBytes)
	_ = conn.SetReadDeadline(time.Time{})

	// Peek 在读到部分字节后失败时仍返回已读部分;短首段照常分类(不足 4 字节
	// 的样本由 classify 的边界判断兜住),只有零字节才算「无首段」。
	if len(head) == 0 {
		if err == nil {
			err = fmt.Errorf("sniff: no data")
		}
		return KindUnknown, nil, fmt.Errorf("sniff: %w", err)
	}
	return classify(head), &sniffedConn{Conn: conn, reader: reader}, nil
}

// classify 是分类规则本体(互斥,顺序即优先级):
//
//	"SSH-"                    → SSH 版本串
//	0x05 + 方法数(≤0x7F)      → socks5 greeting
//	其余                       → HTTP(含未知字节流)
//
// 收窄理由:HTTP 方法与状态行不可能以 "SSH-" 开头,socks5 的 VER 恒 0x05
// 且 NMETHODS 不会超过 0x7F,三类特征不碰撞;被误判进 HTTP 兜底的非 HTTP
// 流由 ReadRequest 失败断连,与合并前「未知流断连」等价。
func classify(head []byte) Kind {
	switch {
	case len(head) >= 4 && string(head[:4]) == "SSH-":
		return KindSSH
	case len(head) >= 2 && head[0] == 0x05 && head[1] <= 0x7F:
		return KindSocks
	default:
		return KindHTTP
	}
}

// sniffedConn 把嗅探期已读字节回放给下游:bufio.Reader 保留全部已缓冲内容,
// Read 从其取而不绕过它,下游看到的字节流与嗅探前完全一致。
type sniffedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
