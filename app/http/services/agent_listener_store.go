package services

// agent_listener_store.go 监听器本地镜像存储(specs/005-event-listener §1/FR-3/FR-4)。
//
// 文件:<agentHome>/.aliang/listeners.json。server 注册表是唯一权威;本镜像只服务
// 本机 UI/CLI 展示与离线只读,可随时全量重建——因此与 mcp.json 的「坏文件拒写」
// 纪律不同:坏文件/缺文件一律按空镜像处理(Warn 不阻断),下次 upsert 覆写修复。
//
// 写盘纪律与 aliangMCPRegistry 同款:SetEscapeHTML(false)+缩进 2+内容未变幂等
// 跳写+MkdirAll+tmp+rename 原子写(writePeerMCPFileAtomic,保留原文件权限)。
// 并发:包级 RWMutex 单写者语义,upsert/remove/save 为「读-改-写」整体单元;
// 缓存懒加载,首次访问从盘读入,此后读走缓存(镜像小,全量驻内存无压力)。
//
// LWW 仲裁(FR-4):乱序推送以 updated_at 定胜负,平局新胜——server 是权威,
// 重放胜过镜像陈旧。时间串为同形态 ISO UTC Z,字典序即时间序。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

// ListenerMirror 是 server 注册表行的 agent 本地镜像(specs/005-event-listener §1;
// snake_case wire 契约,json tag 与 server DB 列名一一对应)。镜像可随时全量重建,
// 是派生物非契约——契约字段以 spec 为准,本结构体只承载展示与 watcher 驱动所需。
type ListenerMirror struct {
	ID              string          `json:"id"`
	AccountID       string          `json:"account_id"`
	Title           string          `json:"title"`
	SourceType      string          `json:"source_type"` // file_tail|process_exit|http_poll|webhook
	SourceDeviceID  string          `json:"source_device_id"`
	SourceParams    json.RawMessage `json:"source_params"`
	PromptTemplate  string          `json:"prompt_template"`
	TargetSessionID string          `json:"target_session_id"`
	Mode            string          `json:"mode"`   // once|recurring
	Status          string          `json:"status"` // active|paused|fired|expired|error
	CooldownMs      int64           `json:"cooldown_ms"`
	ExpiresAt       string          `json:"expires_at"`
	LastFiredAt     string          `json:"last_fired_at"`
	FiredCount      int64           `json:"fired_count"`
	DedupeKey       string          `json:"dedupe_key"`
	OverflowCount   int64           `json:"overflow_count"`
	RejectCount     int64           `json:"reject_count"`
	ErrorNote       string          `json:"error_note"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

// listenerMirrorHome 是本文件取 home 的注入点(与 mcpRegistryHome 同款 seam:
// 测试 swap,禁并行;swap 期间不得有后台调用方)。
var listenerMirrorHome = agentHome

var (
	// listenerMirrorMu 串行化镜像写路径并守护缓存(读走 RLock,写整体持 Lock)。
	listenerMirrorMu sync.RWMutex
	// listenerMirrorCache 镜像驻内存副本;listenerMirrorLoaded 标记是否已从盘
	// 初始化(false 时首个访问触发 read-through)。
	listenerMirrorCache  []ListenerMirror
	listenerMirrorLoaded bool
)

// listenerMirrorPath 镜像落盘路径;home 不可解析时报错(其余文件层问题不在此层报)。
func listenerMirrorPath() (string, error) {
	home := listenerMirrorHome()
	if home == "" {
		return "", fmt.Errorf("agent home unavailable")
	}
	return filepath.Join(home, ".aliang", "listeners.json"), nil
}

// updatedAtLWW LWW 仲裁:同形态 ISO UTC Z 串字典序比较,newTs >= oldTs 即新胜。
// 平局(相等)新胜——server 权威,重放胜过镜像陈旧。空 oldTs(缺字段)视为最旧。
func updatedAtLWW(newTs, oldTs string) bool {
	return newTs >= oldTs
}

// saveListenerMirrors 全量覆写镜像并同步缓存(boot 全量拉取的落盘入口)。
// 字节等价幂等跳写+MkdirAll+tmp+rename 原子写,纪律同 saveAliangMCPRegistryRoot。
func saveListenerMirrors(list []ListenerMirror) error {
	listenerMirrorMu.Lock()
	defer listenerMirrorMu.Unlock()
	return saveListenerMirrorsLocked(list)
}

// upsertListenerMirror 单行 upsert,updated_at LWW 仲裁(FR-4):旧值拒收且
// 文件零改动;平局新胜;缺行直接追加。拒收不是错误(乱序推送是常态)。
func upsertListenerMirror(m ListenerMirror) error {
	listenerMirrorMu.Lock()
	defer listenerMirrorMu.Unlock()
	if err := ensureListenerMirrorCacheLocked(); err != nil {
		return err
	}
	idx := -1
	for i, cur := range listenerMirrorCache {
		if cur.ID == m.ID {
			idx = i
			break
		}
	}
	var next []ListenerMirror
	if idx < 0 {
		next = append(append([]ListenerMirror(nil), listenerMirrorCache...), m)
	} else if updatedAtLWW(m.UpdatedAt, listenerMirrorCache[idx].UpdatedAt) {
		next = append([]ListenerMirror(nil), listenerMirrorCache...)
		next[idx] = m
	} else {
		logger.Debug(fmt.Sprintf("listener-mirror: upsert %s updated_at=%s 旧于镜像 %s,拒收",
			m.ID, m.UpdatedAt, listenerMirrorCache[idx].UpdatedAt))
		return nil // 乱序旧推送:常态路径,不是错误
	}
	return saveListenerMirrorsLocked(next)
}

// removeListenerMirror 按 id 删行;id 不存在时 no-op 不报错(server 补偿重放
// remove 的常态),也不重写文件。
func removeListenerMirror(id string) error {
	listenerMirrorMu.Lock()
	defer listenerMirrorMu.Unlock()
	if err := ensureListenerMirrorCacheLocked(); err != nil {
		return err
	}
	found := false
	next := make([]ListenerMirror, 0, len(listenerMirrorCache))
	for _, cur := range listenerMirrorCache {
		if cur.ID == id {
			found = true
			continue
		}
		next = append(next, cur)
	}
	if !found {
		return nil
	}
	return saveListenerMirrorsLocked(next)
}

// getListenerMirror 按 id 取单行(read-through);返回是否存在。
func getListenerMirror(id string) (ListenerMirror, bool) {
	listenerMirrorMu.RLock()
	if listenerMirrorLoaded {
		defer listenerMirrorMu.RUnlock()
		return findListenerMirrorLocked(id)
	}
	listenerMirrorMu.RUnlock()
	listenerMirrorMu.Lock()
	defer listenerMirrorMu.Unlock()
	if err := ensureListenerMirrorCacheLocked(); err != nil {
		logger.Warn(fmt.Sprintf("listener-mirror: 读缓存初始化失败: %v", err))
		return ListenerMirror{}, false
	}
	return findListenerMirrorLocked(id)
}

// listListenerMirrors 全量读的唯一规范入口(read-through:未初始化时先从盘
// 载入缓存;Task 10 落定合并双读口——原 loadListenerMirrors 已删,home 不可
// 解析等初始化失败在此 Warn+空表,离线容忍 FR-3 不向调用方报错)。
// 恒返回非 nil 空表形态(初始化失败也回 [] 而非 nil——Task 11 代理层据此
// marshal 出 `[]` 而非 `null`,前端拿到可迭代的空数组)。
// 返回副本,调用方改动不污染缓存。SourceParams 逐条防御性拷贝;整表切片
// 调用方只读约定——watcher 高频读路径不做深拷贝。
func listListenerMirrors() []ListenerMirror {
	listenerMirrorMu.RLock()
	if listenerMirrorLoaded {
		defer listenerMirrorMu.RUnlock()
		return snapshotListenerMirrorsLocked()
	}
	listenerMirrorMu.RUnlock()
	listenerMirrorMu.Lock()
	defer listenerMirrorMu.Unlock()
	if err := ensureListenerMirrorCacheLocked(); err != nil {
		logger.Warn(fmt.Sprintf("listener-mirror: 读缓存初始化失败: %v", err))
		return []ListenerMirror{} // 非 nil 空表(见函数头注:代理层 marshal [] 而非 null)
	}
	return snapshotListenerMirrorsLocked()
}

// cloneListenerMirror 浅拷贝一行并防御性拷贝 SourceParams 字节,防调用方改写
// 透传进缓存。调用方须已持锁(读或写)。
func cloneListenerMirror(m ListenerMirror) ListenerMirror {
	if m.SourceParams != nil {
		m.SourceParams = append(json.RawMessage(nil), m.SourceParams...)
	}
	return m
}

// snapshotListenerMirrorsLocked 整表快照(行内 SourceParams 逐条拷贝)。
// 调用方须已持锁(读或写)。
func snapshotListenerMirrorsLocked() []ListenerMirror {
	out := make([]ListenerMirror, 0, len(listenerMirrorCache))
	for _, m := range listenerMirrorCache {
		out = append(out, cloneListenerMirror(m))
	}
	return out
}

// findListenerMirrorLocked 调用方须已持锁(读或写);返回行经 SourceParams
// 防御性拷贝。
func findListenerMirrorLocked(id string) (ListenerMirror, bool) {
	for _, m := range listenerMirrorCache {
		if m.ID == id {
			return cloneListenerMirror(m), true
		}
	}
	return ListenerMirror{}, false
}

// ensureListenerMirrorCacheLocked 缓存未初始化时从盘载入(坏/缺文件按空镜像
// Warn 容忍;home 不可解析返回错误且不置 loaded,环境恢复后下次重试)。
// 调用方须已持写锁。
func ensureListenerMirrorCacheLocked() error {
	if listenerMirrorLoaded {
		return nil
	}
	list, err := readListenerMirrorFile()
	if err != nil {
		return err
	}
	listenerMirrorCache = list
	listenerMirrorLoaded = true
	return nil
}

// readListenerMirrorFile 读盘并解码;缺文件/坏文件/读失败 → 空镜像+Warn
// (镜像可全量重建,离线容忍优先)。调用方无须持锁(纯函数,只碰入参出参)。
func readListenerMirrorFile() ([]ListenerMirror, error) {
	path, err := listenerMirrorPath()
	if err != nil {
		return nil, err
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			logger.Warn(fmt.Sprintf("listener-mirror: %s 不存在,按空镜像处理", path))
			return []ListenerMirror{}, nil
		}
		logger.Warn(fmt.Sprintf("listener-mirror: %s 不可读,按空镜像处理: %v", path, readErr))
		return []ListenerMirror{}, nil
	}
	var list []ListenerMirror
	if err := json.Unmarshal(raw, &list); err != nil {
		logger.Warn(fmt.Sprintf("listener-mirror: %s 坏文件,按空镜像处理(下次写入自动覆写修复): %v", path, err))
		return []ListenerMirror{}, nil
	}
	if list == nil {
		list = []ListenerMirror{} // "null" 文件同缺文件口径
	}
	return list, nil
}

// saveListenerMirrorsLocked 编码→字节等价幂等跳写→MkdirAll→原子写,成功后
// 同步缓存。调用方须已持写锁。纪律同 saveAliangMCPRegistryRoot。
func saveListenerMirrorsLocked(list []ListenerMirror) error {
	path, err := listenerMirrorPath()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, buf.Bytes()) {
		listenerMirrorCache = list
		listenerMirrorLoaded = true
		return nil // 幂等跳写
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := writePeerMCPFileAtomic(path, buf.Bytes()); err != nil {
		return err
	}
	listenerMirrorCache = list
	listenerMirrorLoaded = true
	return nil
}
