package services

// mesh roster 同步(自动组网批次):本机 mesh 身份上送(公钥行 + ssh_ingress
// host key 指纹)+ 同账号 roster 拉取缓存。
//
// 信任锚语义(评审 major 定案):平台 roster 是自动组网的**带外权威**——
// beacon 明文自报无认证,自动准入/拨号凭据必须与 roster 核验后才成立;
// roster 不可得(未拉取/服务端不可达)= 自动组网 fail-closed(候选仅展示)。
// 拉取失败保留既有缓存(fail-open with cached roster):攻击者无法经网络
// 注入 roster 条目,陈旧条目由服务端侧生命周期兜底。
//
// 节流:30s 周期(内部节流,任意调用方随时 MeshRosterSyncTick 都安全),
// in-flight 防重入(atomic CAS),不阻塞任何调用方。

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/cache"
	sshin "aliang.one/nursorgate/inbound/ssh"
	gossh "golang.org/x/crypto/ssh"

	"aliang.one/nursorgate/processor/config"
)

// meshRosterEntry 单条 roster:对端 device_id ↔ mesh 公钥行 ↔ host key 指纹
// (服务端权威绑定)。
type meshRosterEntry struct {
	PubKeyLine string `json:"mesh_pub_key"`
	HostKeyFp  string `json:"host_key_fingerprint"`
}

var (
	meshRosterMu        sync.RWMutex
	meshRosterCache     map[string]meshRosterEntry
	meshRosterFetchedAt time.Time
	meshSyncInFlight    atomic.Bool
	meshSyncStarted     sync.Once
	meshIdentitySig     string // 上送去重签名(公钥行+NUL+host fp;变化才 POST)
)

// meshRosterSyncInterval 同步节流周期(评审:发现即重启不可接受,同理发现
// 即全量拉取也不必——roster 变更频率远低于 beacon 频率)。
const meshRosterSyncInterval = 30 * time.Second

// StartMeshRosterSync boot 启动 roster 同步循环(幂等;两个 boot 形态各调
// 一次,Once 保证单例)。失败仅日志,不影响任何 boot 路径。
func StartMeshRosterSync() {
	meshSyncStarted.Do(func() {
		go func() {
			MeshRosterSyncTick() // 启动即首同步
			ticker := time.NewTicker(meshRosterSyncInterval)
			defer ticker.Stop()
			for range ticker.C {
				MeshRosterSyncTick()
			}
		}()
	})
}

// MeshRosterSyncTick 单次同步(30s 内重复调用为 no-op;in-flight 防重入)。
// 任意调用方(装配 reconcile/boot 循环)随时安全调用。
func MeshRosterSyncTick() {
	if !meshSyncInFlight.CompareAndSwap(false, true) {
		return
	}
	defer meshSyncInFlight.Store(false)
	syncMeshRosterOnce()
}

// syncMeshRosterOnce 一轮同步:①身份上送(变化检测)②roster 拉取。
// mesh 层未配置(Customer.Mesh==nil)=roster 语义不存在,整轮跳过不耗 HTTP。
func syncMeshRosterOnce() {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return
	}
	s := GetSharedAgentService()
	if s == nil {
		return
	}
	pubLine, hostFp := meshIdentityForRoster()
	if pubLine != "" && hostFp != "" {
		s.uploadMeshIdentity(pubLine, hostFp)
	}
	fetchMeshRoster(s, cfg)
}

// meshIdentityForRoster 本机 mesh 身份二元组:公钥行(config private_key
// 派生)+ host key 指纹(cache/ssh 读盘,不触发生成——生成职责单一归
// ingress Server.Start,与装配层指纹同纪律)。任一缺失 → 空串。
func meshIdentityForRoster() (pubLine, hostFp string) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return "", ""
	}
	if pem := strings.TrimSpace(cfg.Customer.Mesh.PrivateKey); pem != "" {
		if signer, err := gossh.ParsePrivateKey([]byte(pem)); err == nil {
			pubLine = strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey())))
		}
	}
	if dir, err := cache.GetCacheSubdir("ssh"); err == nil {
		if info, err := sshin.ReadHostKeyInfo(filepath.Join(dir, sshin.IngressHostKeyFileName)); err == nil {
			hostFp = info.Fingerprint
		}
	}
	return pubLine, hostFp
}

// uploadMeshIdentity 身份上送(变化检测:签名=公钥行+NUL+指纹,未变化不
// POST——roster 目录不需要心跳式重传)。401 恢复链由 callAgentServer 自带。
func (s *AgentService) uploadMeshIdentity(pubLine, hostFp string) {
	sig := pubLine + "\x00" + hostFp
	meshRosterMu.Lock()
	changed := meshIdentitySig != sig
	meshRosterMu.Unlock()
	if !changed {
		return
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return
	}
	deviceID := strings.TrimSpace(s.CurrentDeviceID())
	if deviceID == "" {
		return
	}
	payload := map[string]interface{}{
		"device_id":            deviceID,
		"mesh_pub_key":         pubLine,
		"host_key_fingerprint": hostFp,
	}
	if _, err := s.callAgentServer("POST", cfg.GetAgentMeshIdentityURL(), payload, ""); err != nil {
		return // 失败静默:下轮签名仍视为未上送(changed 保持),自动重试
	}
	meshRosterMu.Lock()
	meshIdentitySig = sig
	meshRosterMu.Unlock()
}

// meshRosterWireEntry roster 拉取响应的 wire 形态(与 PhoneServer
// meshRosterForUser 输出字段对齐)。
type meshRosterWireEntry struct {
	DeviceID   string `json:"device_id"`
	PubKeyLine string `json:"mesh_pub_key"`
	HostKeyFp  string `json:"host_key_fingerprint"`
}

// fetchMeshRoster 拉取同账号 roster 并刷新缓存(失败保留旧缓存——陈旧
// roster 优于空 roster,服务端生命周期兜底)。
func fetchMeshRoster(s *AgentService, cfg *config.Config) {
	raw, err := s.callAgentServer("GET", cfg.GetAgentMeshRosterURL(), nil, "")
	if err != nil {
		return
	}
	var resp struct {
		Roster []meshRosterWireEntry `json:"roster"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	next := make(map[string]meshRosterEntry, len(resp.Roster))
	for _, e := range resp.Roster {
		id := strings.TrimSpace(e.DeviceID)
		if id == "" {
			continue
		}
		next[id] = meshRosterEntry{
			PubKeyLine: strings.TrimSpace(e.PubKeyLine),
			HostKeyFp:  strings.TrimSpace(e.HostKeyFp),
		}
	}
	meshRosterMu.Lock()
	meshRosterCache = next
	meshRosterFetchedAt = time.Now()
	meshRosterMu.Unlock()
}

// MeshRosterEntryFor 自动组网消费方(准入闭包/拨号探针)的 roster 回查:
// device_id ↔ (公钥行, host key 指纹)。未拉取/无条目 = false(自动组网
// fail-closed,候选仍可见)。
func MeshRosterEntryFor(deviceID string) (pubLine, hostFp string, ok bool) {
	meshRosterMu.RLock()
	defer meshRosterMu.RUnlock()
	e, ok := meshRosterCache[strings.TrimSpace(deviceID)]
	if !ok {
		return "", "", false
	}
	return e.PubKeyLine, e.HostKeyFp, true
}

// —— 测试支持(同包直测缓存种子;生产路径只经 fetchMeshRoster 刷新)——

func seedMeshRosterForTest(entries map[string]meshRosterEntry) {
	meshRosterMu.Lock()
	defer meshRosterMu.Unlock()
	meshRosterCache = entries
	meshRosterFetchedAt = time.Now()
}
