package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
)

type RoutingSnapshotBuilder func(*CanonicalRoutingSchema) (any, error)

// routingPersistFileName 是路由开关持久化文件名，落在状态目录
// （<state>/routing.json，与 geoip/ 子目录平级）。内容为 normalize 后的
// canonical JSON，可被 normalizeCanonicalPayload 同一路径原样重新接受。
const routingPersistFileName = "routing.json"

type RoutingApplyResult struct {
	Version uint64
	Hash    string
}

type RoutingApplyCounters struct {
	AttemptCount                uint64
	SuccessCount                uint64
	FailureCount                uint64
	RollbackCount               uint64
	ActiveVersionHashReads      uint64
	ActiveSnapshotVersionHashes uint64
}

type routingApplyStore struct {
	mu            sync.RWMutex
	active        any
	version       uint64
	hash          string
	canonical     *CanonicalRoutingSchema
	attempts      uint64
	successes     uint64
	failures      uint64
	rollbacks     uint64
	versionReads  uint64
	snapshotReads uint64
	// persistMu 串行化持久化写：每次写都重读 store 最新 canonical，
	// 保证并发 Apply 下最终落盘内容与内存最终态一致（后提交者最后写）。
	persistMu sync.Mutex
}

var (
	routingApplyStoreOnce sync.Once
	globalRoutingApply    *routingApplyStore
)

func GetRoutingApplyStore() *routingApplyStore {
	routingApplyStoreOnce.Do(func() {
		globalRoutingApply = &routingApplyStore{}
	})
	return globalRoutingApply
}

// Apply 校验并提交一份 canonical 路由配置：成功后更新内存态并把 canonical JSON
// 原子写入 <state>/routing.json（写失败仅 Warn，不回滚内存态）。
func (s *routingApplyStore) Apply(raw []byte, buildSnapshot RoutingSnapshotBuilder) (*RoutingApplyResult, error) {
	return s.apply(raw, buildSnapshot, true)
}

// apply 是 Apply/LoadPersisted 的共享管线：parse → normalize → validate →
// build snapshot → commit。persist=false 供启动加载复用同一管线但不回写文件。
func (s *routingApplyStore) apply(raw []byte, buildSnapshot RoutingSnapshotBuilder, persist bool) (*RoutingApplyResult, error) {
	atomic.AddUint64(&s.attempts, 1)
	if len(raw) == 0 {
		atomic.AddUint64(&s.failures, 1)
		return nil, fmt.Errorf("routing config payload is empty")
	}
	if buildSnapshot == nil {
		atomic.AddUint64(&s.failures, 1)
		return nil, fmt.Errorf("snapshot builder is required")
	}

	parsed, err := parseRoutingPayload(raw)
	if err != nil {
		atomic.AddUint64(&s.failures, 1)
		return nil, err
	}
	postParse := true

	normalized, err := normalizeRoutingPayload(parsed, raw)
	if err != nil {
		atomic.AddUint64(&s.failures, 1)
		if postParse {
			atomic.AddUint64(&s.rollbacks, 1)
		}
		return nil, err
	}

	if err := normalized.Validate(); err != nil {
		atomic.AddUint64(&s.failures, 1)
		if postParse {
			atomic.AddUint64(&s.rollbacks, 1)
		}
		return nil, err
	}

	snapshot, err := buildSnapshot(normalized)
	if err != nil {
		atomic.AddUint64(&s.failures, 1)
		if postParse {
			atomic.AddUint64(&s.rollbacks, 1)
		}
		return nil, fmt.Errorf("build snapshot failed: %w", err)
	}

	hash, err := hashCanonicalRoutingSchema(normalized)
	if err != nil {
		atomic.AddUint64(&s.failures, 1)
		if postParse {
			atomic.AddUint64(&s.rollbacks, 1)
		}
		return nil, err
	}

	canonicalCopy, err := cloneCanonicalRoutingSchema(normalized)
	if err != nil {
		atomic.AddUint64(&s.failures, 1)
		if postParse {
			atomic.AddUint64(&s.rollbacks, 1)
		}
		return nil, err
	}

	s.mu.Lock()

	s.active = snapshot
	s.canonical = canonicalCopy
	s.version++
	s.hash = hash
	result := &RoutingApplyResult{Version: s.version, Hash: s.hash}
	atomic.AddUint64(&s.successes, 1)

	s.mu.Unlock()

	if persist {
		s.persistActiveCanonical()
	}

	return result, nil
}

// LoadPersisted 在进程启动时从 <state>/routing.json 恢复路由配置（含用户
// egress 开关状态）。返回值 loaded：
//   - true：文件存在且已成功装载进 store（或 store 本就已有运行态，不覆盖）；
//   - false + nil error：文件不存在，调用方走既有 bootstrap 推导；
//   - false + error：文件损坏/校验失败等，store 保持为空（等价 bootstrap）。
//
// 装载走与 Apply 完全相同的 normalize/Validate 管线（不回写文件）。
func (s *routingApplyStore) LoadPersisted(buildSnapshot RoutingSnapshotBuilder) (bool, error) {
	if buildSnapshot == nil {
		return false, fmt.Errorf("snapshot builder is required")
	}

	s.mu.RLock()
	hasActiveState := s.canonical != nil
	s.mu.RUnlock()
	if hasActiveState {
		// store 已有运行态（启动早期被 Apply 过或重复调用）：保留较新状态，不回退到磁盘。
		return true, nil
	}

	path, err := routingPersistFilePath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read persisted routing config failed: %w", err)
	}
	if len(raw) == 0 {
		return false, fmt.Errorf("persisted routing config is empty: %s", path)
	}

	if _, err := s.apply(raw, buildSnapshot, false); err != nil {
		return false, fmt.Errorf("load persisted routing config failed: %w", err)
	}
	return true, nil
}

// persistActiveCanonical 把 store 当前 canonical 原子落盘（tmp + rename）。
// 任何失败仅 Warn：内存态已是事实源，下次成功 Apply 会再次尝试落盘。
func (s *routingApplyStore) persistActiveCanonical() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	canonical := s.ActiveCanonicalSchema()
	if canonical == nil {
		return
	}

	path, err := routingPersistFilePath()
	if err != nil {
		logger.Warn(fmt.Sprintf("Resolve routing persistence path failed (in-memory state kept): %v", err))
		return
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		logger.Warn(fmt.Sprintf("Marshal canonical routing schema for persistence failed: %v", err))
		return
	}

	if err := atomicWriteFile(path, encoded); err != nil {
		logger.Warn(fmt.Sprintf("Persist routing config to %s failed (in-memory state kept): %v", path, err))
	}
}

// routingPersistFilePath 返回 <state>/routing.json 的绝对路径（目录由
// cache.GetCacheDir 保证存在）。
func routingPersistFilePath() (string, error) {
	return cache.GetCacheFile(routingPersistFileName)
}

// atomicWriteFile 以 tmp 文件 + rename 的方式原子写入，tmp 与目标同目录保证
// rename 原子性；权限对齐 cache 包约定（0o666，实际受 umask 收敛）。
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file failed: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file failed: %w", err)
	}
	if err := tmp.Chmod(cache.DefaultFilePermissions); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file failed: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place failed: %w", err)
	}
	tmpName = "" // rename 成功，tmp 已不存在，无需清理
	return nil
}

// LoadPersistedIntoStore 是三条启动路径（core/start/tray）的统一入口：
// 在任何连接处理之前调用，恢复用户上次的 egress 开关状态。
// 无文件 → 静默跳过（走既有 bootstrap 推导）；失败 → Warn 不阻断启动。
// 返回的 error 仅供调用方追加记录，不得作为启动失败依据。
func LoadPersistedIntoStore(buildSnapshot RoutingSnapshotBuilder) error {
	loaded, err := GetRoutingApplyStore().LoadPersisted(buildSnapshot)
	if err != nil {
		logger.Warn(fmt.Sprintf("Load persisted routing config failed (bootstrap derivation applies this run): %v", err))
		return err
	}
	if loaded {
		logger.Info("Persisted routing config loaded (user egress switches restored)")
	} else {
		logger.Debug("No persisted routing config found; bootstrap routing derivation applies")
	}
	return nil
}

func (s *routingApplyStore) ActiveVersionHash() (uint64, string) {
	atomic.AddUint64(&s.versionReads, 1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, s.hash
}

func (s *routingApplyStore) ActiveSnapshotVersionHash() (any, uint64, string) {
	atomic.AddUint64(&s.snapshotReads, 1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active, s.version, s.hash
}

func (s *routingApplyStore) ApplyCounters() RoutingApplyCounters {
	return RoutingApplyCounters{
		AttemptCount:                atomic.LoadUint64(&s.attempts),
		SuccessCount:                atomic.LoadUint64(&s.successes),
		FailureCount:                atomic.LoadUint64(&s.failures),
		RollbackCount:               atomic.LoadUint64(&s.rollbacks),
		ActiveVersionHashReads:      atomic.LoadUint64(&s.versionReads),
		ActiveSnapshotVersionHashes: atomic.LoadUint64(&s.snapshotReads),
	}
}

func (s *routingApplyStore) ActiveSnapshot() any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *routingApplyStore) ActiveCanonicalSchema() *CanonicalRoutingSchema {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.canonical == nil {
		return nil
	}
	clone, err := cloneCanonicalRoutingSchema(s.canonical)
	if err != nil {
		return nil
	}
	return clone
}

// ActiveEgressSwitches 返回活跃 canonical 的 toAliang/toSocks 开关的无拷贝读数，
// 供高频决策路径（每连接编译运行时快照）避免整份 schema 的 clone 开销。
// ok=false 表示 store 尚无 canonical，调用方应回落既有默认。
// 安全性：canonical 提交后只整体替换、从不原地修改，RLock 下读布尔字段无竞争。
func (s *routingApplyStore) ActiveEgressSwitches() (aliangEnabled, socksEnabled bool, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.canonical == nil {
		return false, false, false
	}
	return s.canonical.Egress.ToAliang.Enabled, s.canonical.Egress.ToSocks.Enabled, true
}

func ResetRoutingApplyStoreForTest() {
	store := GetRoutingApplyStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.active = nil
	store.version = 0
	store.hash = ""
	store.canonical = nil
	atomic.StoreUint64(&store.attempts, 0)
	atomic.StoreUint64(&store.successes, 0)
	atomic.StoreUint64(&store.failures, 0)
	atomic.StoreUint64(&store.rollbacks, 0)
	atomic.StoreUint64(&store.versionReads, 0)
	atomic.StoreUint64(&store.snapshotReads, 0)
}

func parseRoutingPayload(raw []byte) (map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("failed to parse routing schema JSON: %w", err)
	}
	return root, nil
}

func normalizeRoutingPayload(root map[string]json.RawMessage, raw []byte) (*CanonicalRoutingSchema, error) {
	if _, ok := root["ingress"]; ok {
		return normalizeCanonicalPayload(raw)
	}
	return nil, fmt.Errorf("non-canonical routing payload is not supported; use canonical keys ingress/egress/routing with targets direct|toSocks|toAliang")
}

func hashCanonicalRoutingSchema(cfg *CanonicalRoutingSchema) (string, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to hash canonical routing schema: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneCanonicalRoutingSchema(cfg *CanonicalRoutingSchema) (*CanonicalRoutingSchema, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to clone canonical routing schema: %w", err)
	}
	var clone CanonicalRoutingSchema
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, fmt.Errorf("failed to clone canonical routing schema: %w", err)
	}
	return &clone, nil
}
