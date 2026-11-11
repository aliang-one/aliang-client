// mesh_dns.go — mesh-exit dns 代解析(SPEC §9):A 侧 {"t":"dns"} 通道在
// B 侧读 [2B 大端长度][DNS 报文] 帧 → 系统解析器按类型解析 → 合成响应帧
// 回写 → 关(单问单答;不进入站管线,无 DNS 二跳)。
//
// 全部解析面自 processor/tcp/mesh_inbound.go 整段搬运(签名不含 meshproto
// 依赖,F1 删除源侧后两处合一):函数与常量名保持同源未导出形态,doc 注释
// 随搬;两侧改动需同步(合并批次以集成测试互锁)。
package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"

	"github.com/miekg/dns"
	gossh "golang.org/x/crypto/ssh"
)

// LocalDNSResolver 抽象 B 侧 dns 代解析所需的最小解析面(*net.Resolver 原生
// 满足,测试注 fake 不发真查询)。
//
// 取舍(design §6 层1 定案,随源搬运):走系统解析器(net.Resolver)而非
// 探测系统上游 DNS 地址再自办递归——Windows 无 stdlib 上游地址 API,
// net.Resolver 走系统解析器全平台可用;代价是 B 侧解析结果不经过 B 的代理
// 规则(与 TUN 本机 DNS 走系统解析器同口径)。
type LocalDNSResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupSRV(ctx context.Context, service, proto, name string) (cname string, addrs []*net.SRV, err error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// meshDNSResolveTimeout 界定 B 侧 dns 代解析交换总窗口(查询帧读取+系统
// 解析+响应回写;A 侧同款 5s,dnsForwardExchangeTimeout)。var 而非 const 是
// 刻意的测试缝(覆写+恢复,非并行安全),源侧同款先例 meshHeaderReadTimeout。
var meshDNSResolveTimeout = 5 * time.Second

// meshDNSAnswerTTL 是 B 侧合成响应的固定 TTL(design §6 层1 定案:30s,
// 短 TTL 让 A 侧客户端缓存快速过期,权威性由 B 侧系统解析器保证)。
const meshDNSAnswerTTL = 30

// meshDNSResolver 是 mesh-exit dns 代解析所用解析器(net.Resolver 系统解析
// 器,SPEC §9 取舍注释随搬);var 仅为测试注入 fake。
var meshDNSResolver LocalDNSResolver = &net.Resolver{}

// serveMeshDNSExit 处理 {"t":"dns"} channel:读 [2B 长度][DNS 报文] 帧 →
// 解析 → 写 [2B 长度][响应] → 关(SPEC §9;不进入站管线,无 DNS 二跳)。
// 交换总窗口 meshDNSResolveTimeout 罩全流程:gossh.Channel 无 deadline 语义
// (channel.go channelConn 同款限制),读取侧改用 AfterFunc 到点关通道实现
// 窗口(源侧 handleMeshDNSTarget 的 SetDeadline 等效替身),解析窗口另有
// ctx 超时兜底;慢速/静默对端不无限占住 goroutine。
func serveMeshDNSExit(ch gossh.Channel) {
	defer ch.Close()
	resolver := meshDNSResolver
	if resolver == nil {
		resolver = &net.Resolver{}
	}
	window := time.AfterFunc(meshDNSResolveTimeout, func() { _ = ch.Close() })
	defer window.Stop()

	query, err := readDNSLengthFrame(ch)
	if err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit dns: read frame: %v", err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), meshDNSResolveTimeout)
	defer cancel()
	if werr := writeDNSLengthFrame(ch, resolveDNSQuery(ctx, resolver, query)); werr != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit dns: write frame: %v", werr))
	}
}

// readDNSLengthFrame 读一条 [2B 大端长度][载荷] 帧(dns 目标专用线格式,
// design §6 层1,随源搬运)。零长度帧按协议错误处理(空 DNS 报文非法)。
func readDNSLengthFrame(r io.Reader) ([]byte, error) {
	var frame [2]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(frame[:])
	if n == 0 {
		return nil, errors.New("mesh dns: empty frame")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// writeDNSLengthFrame 写一条 [2B 大端长度][载荷] 帧,与 readDNSLengthFrame
// 对偶(随源搬运)。
func writeDNSLengthFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > math.MaxUint16 {
		return fmt.Errorf("mesh dns: frame payload size %d out of range", len(payload))
	}
	var frame [2]byte
	binary.BigEndian.PutUint16(frame[:], uint16(len(payload)))
	if _, err := w.Write(frame[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// resolveDNSQuery 把查询报文解析为响应报文(永不返回 nil:任何失败都折叠为
// SERVFAIL 响应——查询 ID 尽力回显,拆不开时以头两字节为 ID 合成裸头)。
func resolveDNSQuery(ctx context.Context, resolver LocalDNSResolver, query []byte) []byte {
	var req dns.Msg
	if err := req.Unpack(query); err != nil || len(req.Question) == 0 {
		return servfailDNSResponse(query)
	}
	resp := new(dns.Msg)
	resp.SetReply(&req)
	resp.RecursionAvailable = true

	answers, resolveErr := resolveDNSQuestion(ctx, resolver, req.Question[0])
	if resolveErr != nil {
		resp.SetRcode(&req, dnsRcodeForResolveError(resolveErr))
	} else {
		resp.Answer = answers
	}
	out, err := resp.Pack()
	if err != nil {
		return servfailDNSResponse(query)
	}
	return out
}

// dnsRcodeForResolveError 区分负应答与暂态错误:net.DNSError.IsNotFound
// (上游 NXDOMAIN/无此主机)是有效负应答,回 RcodeNameError 让客户端负缓存
// 不重试;其余错误 → SERVFAIL(暂态错误,客户端可重试)。离线冲浪的广告
// 域名是 NXDOMAIN 重灾区,折叠成 SERVFAIL 会引发重试风暴(质量评审精化)。
func dnsRcodeForResolveError(err error) int {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return dns.RcodeNameError
	}
	return dns.RcodeServerFailure
}

// resolveDNSQuestion 按查询类型分发(design §6 层1 定案的类型矩阵):
// A/AAAA→LookupIP;PTR→LookupAddr(查询名反算点分地址);MX/SRV/TXT→对应
// Lookup;其余类型一律报错(上层折叠为 SERVFAIL)。多 Question 查询按首个
// 处理(真实 DNS 客户端恒单问)。
func resolveDNSQuestion(ctx context.Context, resolver LocalDNSResolver, q dns.Question) ([]dns.RR, error) {
	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA:
		network := "ip4"
		qtype := dns.TypeA
		if q.Qtype == dns.TypeAAAA {
			network, qtype = "ip6", dns.TypeAAAA
		}
		ips, err := resolver.LookupIP(ctx, network, q.Name)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(ips))
		for _, ip := range ips {
			if q.Qtype == dns.TypeA && ip.To4() == nil {
				continue
			}
			if q.Qtype == dns.TypeAAAA && (ip.To4() != nil || ip.To16() == nil) {
				continue
			}
			if q.Qtype == dns.TypeA {
				answers = append(answers, &dns.A{Hdr: dnsRRHeader(q, qtype), A: ip.To4()})
			} else {
				answers = append(answers, &dns.AAAA{Hdr: dnsRRHeader(q, qtype), AAAA: ip.To16()})
			}
		}
		return answers, nil
	case dns.TypePTR:
		addr, ok := dnsPTRNameToAddr(q.Name)
		if !ok {
			return nil, fmt.Errorf("mesh dns: unparseable PTR name %q", q.Name)
		}
		names, err := resolver.LookupAddr(ctx, addr)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(names))
		for _, name := range names {
			answers = append(answers, &dns.PTR{Hdr: dnsRRHeader(q, dns.TypePTR), Ptr: dns.Fqdn(name)})
		}
		return answers, nil
	case dns.TypeMX:
		mxs, err := resolver.LookupMX(ctx, q.Name)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(mxs))
		for _, mx := range mxs {
			answers = append(answers, &dns.MX{
				Hdr:        dnsRRHeader(q, dns.TypeMX),
				Preference: mx.Pref,
				Mx:         dns.Fqdn(mx.Host),
			})
		}
		return answers, nil
	case dns.TypeSRV:
		service, proto, target := splitSRVName(q.Name)
		_, addrs, err := resolver.LookupSRV(ctx, service, proto, target)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(addrs))
		for _, srv := range addrs {
			answers = append(answers, &dns.SRV{
				Hdr:      dnsRRHeader(q, dns.TypeSRV),
				Priority: srv.Priority,
				Weight:   srv.Weight,
				Port:     srv.Port,
				Target:   dns.Fqdn(srv.Target),
			})
		}
		return answers, nil
	case dns.TypeTXT:
		txts, err := resolver.LookupTXT(ctx, q.Name)
		if err != nil {
			return nil, err
		}
		return []dns.RR{&dns.TXT{Hdr: dnsRRHeader(q, dns.TypeTXT), Txt: txts}}, nil
	default:
		return nil, fmt.Errorf("mesh dns: unsupported qtype %d(%s)", q.Qtype, dns.TypeToString[q.Qtype])
	}
}

// dnsRRHeader 构造答案 RR 头:名称回显查询名、TTL 固定 meshDNSAnswerTTL。
func dnsRRHeader(q dns.Question, qtype uint16) dns.RR_Header {
	return dns.RR_Header{Name: q.Name, Rrtype: qtype, Class: dns.ClassINET, Ttl: meshDNSAnswerTTL}
}

// dnsPTRNameToAddr 把 PTR 查询名还原为点分文本地址(net.Resolver.LookupAddr
// 的入参形态):v4 反转 in-addr.arpa 四段;v6 反转 ip6.arpa 32 个 nibble 并
// 按 4 位分组拼冒号十六进制。
func dnsPTRNameToAddr(name string) (string, bool) {
	labels := dns.SplitDomainName(name)
	if len(labels) < 3 {
		return "", false
	}
	switch suffix := labels[len(labels)-2:]; {
	case suffix[0] == "in-addr" && suffix[1] == "arpa":
		n := len(labels) - 2
		if n != 4 {
			return "", false
		}
		return labels[3] + "." + labels[2] + "." + labels[1] + "." + labels[0], true
	case suffix[0] == "ip6" && suffix[1] == "arpa":
		nibbles := labels[:len(labels)-2]
		if len(nibbles) != 32 {
			return "", false
		}
		var b strings.Builder
		for i := 31; i >= 0; i-- {
			b.WriteString(nibbles[i])
			if i > 0 && i%4 == 0 {
				b.WriteByte(':')
			}
		}
		return b.String(), true
	default:
		return "", false
	}
}

// splitSRVName 拆 _service._proto.name 形态的 SRV 查询名为 LookupSRV 三元
// 组;不带下划线前缀的形态按整名直查(service/proto 传空,stdlib 直查语义)。
func splitSRVName(name string) (service, proto, target string) {
	labels := dns.SplitDomainName(name)
	if len(labels) >= 3 && strings.HasPrefix(labels[0], "_") && strings.HasPrefix(labels[1], "_") {
		return strings.TrimPrefix(labels[0], "_"), strings.TrimPrefix(labels[1], "_"), strings.Join(labels[2:], ".")
	}
	return "", "", strings.TrimSuffix(name, ".")
}

// servfailDNSResponse 为 query 合成 SERVFAIL 响应报文:能拆包则以 SetRcode
// 保留 Question 回显;拆不开(至少头两字节可用)以查询头两字节为 ID 合成
// 裸 SERVFAIL 头;连 2 字节都没有则返回裸空响应(上层仍会写出帧,极端畸形
// 输入的兜底)。
func servfailDNSResponse(query []byte) []byte {
	var req dns.Msg
	if err := req.Unpack(query); err == nil {
		resp := new(dns.Msg)
		resp.SetRcode(&req, dns.RcodeServerFailure)
		if out, err := resp.Pack(); err == nil {
			return out
		}
	}
	resp := new(dns.Msg)
	resp.Response = true
	resp.Rcode = dns.RcodeServerFailure
	if len(query) >= 2 {
		resp.Id = binary.BigEndian.Uint16(query[:2])
	}
	out, err := resp.Pack()
	if err != nil {
		return make([]byte, 12) // 12 字节 DNS 头裸 SERVFAIL(Id=0),不再向下失败
	}
	return out
}
