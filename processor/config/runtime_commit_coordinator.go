package config

import (
	"fmt"
	"os"
	"sync"
)

type EffectiveConfigSnapshot struct {
	UUID     string
	Software string
	Name     string
	FilePath string
	Version  string
	Format   string
	Content  string
}

func (s *EffectiveConfigSnapshot) clone() *EffectiveConfigSnapshot {
	if s == nil {
		return nil
	}
	copyValue := *s
	return &copyValue
}

type EffectiveConfigCommitResult struct {
	Version uint64
}

type EffectiveConfigCommitCoordinator struct {
	mu        sync.RWMutex
	active    *EffectiveConfigSnapshot
	committed *EffectiveConfigSnapshot
	version   uint64
}

var (
	effectiveConfigCommitCoordinatorOnce   sync.Once
	globalEffectiveConfigCommitCoordinator *EffectiveConfigCommitCoordinator
)

func GetEffectiveConfigCommitCoordinator() *EffectiveConfigCommitCoordinator {
	effectiveConfigCommitCoordinatorOnce.Do(func() {
		globalEffectiveConfigCommitCoordinator = &EffectiveConfigCommitCoordinator{}
	})
	return globalEffectiveConfigCommitCoordinator
}

func (c *EffectiveConfigCommitCoordinator) Commit(
	next *EffectiveConfigSnapshot,
	persistFile func(*EffectiveConfigSnapshot) error,
	persistDB func(*EffectiveConfigSnapshot) error,
) (*EffectiveConfigCommitResult, error) {
	if next == nil {
		return nil, fmt.Errorf("effective config snapshot is required")
	}
	if persistFile == nil {
		return nil, fmt.Errorf("file persist callback is required")
	}
	if persistDB == nil {
		return nil, fmt.Errorf("db persist callback is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	previousActive := c.active.clone()
	nextSnapshot := next.clone()
	c.active = nextSnapshot

	if err := persistFile(nextSnapshot); err != nil {
		c.active = previousActive
		return nil, fmt.Errorf("commit failed at file stage: %w", err)
	}

	if err := persistDB(nextSnapshot); err != nil {
		rollbackTarget := c.committed.clone()
		c.active = rollbackTarget

		rollbackErr := c.rollbackFileToCommittedSnapshot(rollbackTarget, nextSnapshot.FilePath, persistFile)
		if rollbackErr != nil {
			return nil, fmt.Errorf("commit failed at db stage: %w (file rollback failed: %v)", err, rollbackErr)
		}

		return nil, fmt.Errorf("commit failed at db stage: %w", err)
	}

	c.committed = nextSnapshot.clone()
	c.version++

	return &EffectiveConfigCommitResult{Version: c.version}, nil
}

func (c *EffectiveConfigCommitCoordinator) rollbackFileToCommittedSnapshot(
	committed *EffectiveConfigSnapshot,
	failedFilePath string,
	persistFile func(*EffectiveConfigSnapshot) error,
) error {
	if committed != nil {
		return persistFile(committed)
	}

	if failedFilePath == "" {
		return nil
	}

	if err := os.Remove(failedFilePath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (c *EffectiveConfigCommitCoordinator) ActiveSnapshot() *EffectiveConfigSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active.clone()
}

func (c *EffectiveConfigCommitCoordinator) LastCommittedSnapshot() *EffectiveConfigSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.committed.clone()
}

func (c *EffectiveConfigCommitCoordinator) Version() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

func ResetEffectiveConfigCommitCoordinatorForTest() {
	coordinator := GetEffectiveConfigCommitCoordinator()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.active = nil
	coordinator.committed = nil
	coordinator.version = 0
}
