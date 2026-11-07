// Package ssh 实现基于 SSH 链(跳板+终点)的出站代理。
package ssh

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// HostEntry 是 known_hosts 里的一台 SSH 主机(链上任一节点:跳板或终点)。
type HostEntry struct {
	Host        string    `json:"host"`        // "host:port"
	Fingerprint string    `json:"fingerprint"` // SHA256:base64
	Algorithm   string    `json:"algorithm"`   // 如 ssh-ed25519
	AddedAt     time.Time `json:"added_at"`
}

// KnownHosts 是 TOFU 指纹库:首连写入,复连校验,不匹配拒连。
type KnownHosts struct {
	mu   sync.Mutex
	path string
}

func NewKnownHosts(path string) *KnownHosts {
	return &KnownHosts{path: path}
}

// CallbackFor 返回指定 host:port 的 TOFU 校验回调。
// x/crypto 会把 hostname 的端口剥掉再传进来,所以 key 由调用方(链构建器,
// 知道每个节点的准确 host:port)闭包注入,不从参数拼。
func (k *KnownHosts) CallbackFor(hostport string) gossh.HostKeyCallback {
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		fp := gossh.FingerprintSHA256(key)
		k.mu.Lock()
		defer k.mu.Unlock()

		entries, err := k.loadLocked()
		if err != nil {
			return fmt.Errorf("load known_hosts: %w", err)
		}
		for _, e := range entries {
			if e.Host != hostport {
				continue
			}
			if e.Fingerprint == fp {
				return nil
			}
			return fmt.Errorf("ssh host key mismatch for %s: 已知 %s,实际 %s(可能是服务器重装或中间人;可在仪表盘「服务器指纹」删除该条目后重连)", hostport, e.Fingerprint, fp)
		}
		entries = append(entries, HostEntry{
			Host:        hostport,
			Fingerprint: fp,
			Algorithm:   key.Type(),
			AddedAt:     time.Now(),
		})
		return k.saveLocked(entries)
	}
}

func (k *KnownHosts) List() []HostEntry {
	k.mu.Lock()
	defer k.mu.Unlock()
	entries, err := k.loadLocked()
	if err != nil {
		return nil
	}
	return entries
}

func (k *KnownHosts) Remove(hostport string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	entries, err := k.loadLocked()
	if err != nil {
		return err
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.Host != hostport {
			kept = append(kept, e)
		}
	}
	return k.saveLocked(kept)
}

func (k *KnownHosts) Clear() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.saveLocked(nil)
}

func (k *KnownHosts) loadLocked() ([]HostEntry, error) {
	data, err := os.ReadFile(k.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []HostEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// saveLocked 原子写:临时文件+rename,避免半行写入导致指纹库损坏。
func (k *KnownHosts) saveLocked(entries []HostEntry) error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.path)
}
