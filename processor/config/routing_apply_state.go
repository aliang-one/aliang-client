package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

type RoutingSnapshotBuilder func(*CanonicalRoutingSchema) (any, error)

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

func (s *routingApplyStore) Apply(raw []byte, buildSnapshot RoutingSnapshotBuilder) (*RoutingApplyResult, error) {
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
	defer s.mu.Unlock()

	s.active = snapshot
	s.canonical = canonicalCopy
	s.version++
	s.hash = hash
	atomic.AddUint64(&s.successes, 1)

	return &RoutingApplyResult{Version: s.version, Hash: s.hash}, nil
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
