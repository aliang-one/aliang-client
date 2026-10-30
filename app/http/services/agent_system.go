package services

// 设备画像冷层/热层（spec 2026-10-29-device-peer-link-and-profile-design §4.2/§4.3）。
//
// 为什么要这两层：LLM 要在「同一账号的另一台设备」上执行命令，第一步必须知道
// 自己有哪些设备、哪台是 Windows、哪台有 GPU/内存多大。hello 基线只有
// platform 一个粗标签（"windows-amd64"），不足以让模型选目标，更不足以让
// 服务端判断两台机是否同内网（阶段 2 的 LAN 选路前提）。
//
//   - 冷层 system：OS/内核/架构/CPU 型号/内存总量。进程生命周期内不变
//     （换机=新进程），故 sync.Once 采集一次，零重复开销。
//   - 热层 net：非回环 IPv4 接口地址 + 前缀长度。网卡可热插拔（插网线、连 WiFi），
//     故 2s TTL 缓存，与 cpuSampler 同套路（不阻塞心跳 goroutine）。
//   - profile_hash：冷层规范化摘要，供服务端识别「清单没变」的重复 hello。
//
// 纪律：全部 best-effort，单项失败即 omitempty 跳过——任何一台机器的
// gopsutil 调用异常都不允许阻塞 hello/heartbeat（与 agent_load.go 同款降级）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// agentSystemSnapshot 是 hello 的 system 子对象：一台机的固定身份。
type agentSystemSnapshot struct {
	OSName        string `json:"os_name,omitempty"`
	OSVersion     string `json:"os_version,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
	Arch          string `json:"arch,omitempty"`
	CPUModel      string `json:"cpu_model,omitempty"`
	MemTotalBytes uint64 `json:"mem_total_bytes,omitempty"`
}

var (
	agentSystemOnce  sync.Once
	agentSystemValue agentSystemSnapshot
)

// collectAgentSystemSnapshot 采集冷层画像。首次调用后缓存到进程结束——
// host.Info()/cpu.Info() 在部分平台要走 sysctl/注册表且可能几十毫秒，
// 挂在 10s 心跳上重复采是纯浪费。
func collectAgentSystemSnapshot() agentSystemSnapshot {
	agentSystemOnce.Do(func() {
		var snap agentSystemSnapshot
		if info, err := host.Info(); err == nil && info != nil {
			snap.OSName = strings.TrimSpace(info.OS)
			snap.OSVersion = strings.TrimSpace(info.PlatformVersion)
			if snap.OSVersion == "" {
				snap.OSVersion = strings.TrimSpace(info.Platform)
			}
			snap.KernelVersion = strings.TrimSpace(info.KernelVersion)
			snap.Arch = strings.TrimSpace(info.KernelArch)
		}
		if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
			// 多路/异构核会有多条；取首条的型号即可（品牌串一致）。
			snap.CPUModel = strings.TrimSpace(infos[0].ModelName)
		}
		if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
			snap.MemTotalBytes = vm.Total
		}
		agentSystemValue = snap
	})
	return agentSystemValue
}

// agentNetInterface 是单个非回环 IPv4 接口。
type agentNetInterface struct {
	Name      string `json:"name"`
	IPv4      string `json:"ipv4"`
	PrefixLen int    `json:"prefix_len"`
}

// agentNetSnapshot 是心跳的 net 子对象：会随网卡热插拔变化的网络身份。
// 阶段 2 的 LAN 判定（两端接口子网重叠 + 出口 IP 相同）全部依赖它。
type agentNetSnapshot struct {
	Interfaces  []agentNetInterface `json:"interfaces"`
	CollectedAt string              `json:"collected_at,omitempty"`
}

const (
	// agentNetCacheTTL 是接口枚举的缓存窗口。net.Interfaces()+Addrs() 在
	// macOS/Windows 上要走系统调用（实测单次 ms 级），心跳 10s 一拍重复采
	// 属浪费；2s TTL 既保证插拔后两拍内可见，又让同一拍的 hello+heartbeat
	// 共用一次枚举。
	agentNetCacheTTL = 2 * time.Second
)

var (
	agentNetMu     sync.Mutex
	agentNetValue  agentNetSnapshot
	agentNetExpiry time.Time
)

// collectAgentNetSnapshot 返回非回环 IPv4 接口清单（2s TTL 缓存）。
// 只取 IPv4：阶段 2 的 CIDR 重叠判定只在 v4 上做（spec §6.1），v6 会引入
// 链路本地/临时地址等噪音，本期不收。
func collectAgentNetSnapshot() agentNetSnapshot {
	agentNetMu.Lock()
	defer agentNetMu.Unlock()
	now := time.Now()
	if now.Before(agentNetExpiry) && agentNetValue.CollectedAt != "" {
		return agentNetValue
	}

	snap := agentNetSnapshot{
		Interfaces:  []agentNetInterface{},
		CollectedAt: now.UTC().Format(time.RFC3339),
	}
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			// 跳过 Down 的网卡（未插线的以太口）与回环：它们对 LAN 判定
			// 只有噪音价值。
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if !ok || ipNet.IP == nil {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
					continue
				}
				ones, _ := ipNet.Mask.Size()
				snap.Interfaces = append(snap.Interfaces, agentNetInterface{
					Name:      iface.Name,
					IPv4:      ip4.String(),
					PrefixLen: ones,
				})
			}
		}
	}
	agentNetValue = snap
	agentNetExpiry = now.Add(agentNetCacheTTL)
	return snap
}

// agentProfileHash 计算冷层规范化摘要（sha256 前 16 字节 hex）。
//
// 口径（故意不含高频字段）：
//   - 含：system（OS/内核/架构/CPU/内存）、platform、capabilities、工具清单
//     的 id+version、授权目录。这些变一次意味着「设备能力画像变了」。
//   - 不含：projects（last_active 随对话每回合都变）、history、load。
//     把它们算进 hash 会让 hash 每拍都变，去重价值归零。
//
// 服务端可以用它跳过重复的清单落库；缺省时老服务端会忽略该字段（向前兼容）。
func agentProfileHash(platform string, capabilities []string, toolVersions []string, authorizedDirs []string) string {
	payload := struct {
		System       agentSystemSnapshot `json:"system"`
		Platform     string              `json:"platform"`
		Capabilities []string            `json:"capabilities"`
		Tools        []string            `json:"tools"`
		Authorized   []string            `json:"authorized_dirs"`
	}{
		System:       collectAgentSystemSnapshot(),
		Platform:     platform,
		Capabilities: append([]string(nil), capabilities...),
		Tools:        append([]string(nil), toolVersions...),
		Authorized:   append([]string(nil), authorizedDirs...),
	}
	sort.Strings(payload.Capabilities)
	sort.Strings(payload.Tools)
	sort.Strings(payload.Authorized)
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// agentSystemSummary 把 system 快照压成一行式摘要（device_info / device_list
// 共用）。缺字段留空，不占位。
func agentSystemSummary(sys agentSystemSnapshot) string {
	parts := make([]string, 0, 5)
	if sys.OSName != "" {
		os := sys.OSName
		if sys.OSVersion != "" {
			os += " " + sys.OSVersion
		}
		parts = append(parts, os)
	}
	if sys.Arch != "" {
		parts = append(parts, sys.Arch)
	}
	if sys.KernelVersion != "" {
		parts = append(parts, "kernel "+sys.KernelVersion)
	}
	if sys.CPUModel != "" {
		parts = append(parts, sys.CPUModel)
	}
	if sys.MemTotalBytes > 0 {
		parts = append(parts, fmt.Sprintf("%d MiB RAM", sys.MemTotalBytes>>20))
	}
	return strings.Join(parts, ", ")
}

// agentNetSummary 把 net 快照压成一行式摘要（内网地址列表）。
func agentNetSummary(netSnap agentNetSnapshot) string {
	if len(netSnap.Interfaces) == 0 {
		return ""
	}
	parts := make([]string, 0, len(netSnap.Interfaces))
	for _, iface := range netSnap.Interfaces {
		parts = append(parts, fmt.Sprintf("%s=%s/%d", iface.Name, iface.IPv4, iface.PrefixLen))
	}
	return strings.Join(parts, ", ")
}
