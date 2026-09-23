package services

import (
	"bufio"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// loadClaudePeerToken reads the peer auth token for a pid from
// ~/.claude/sessions/<pid>.<sha>.key (CC >= 2.1.224). Read on demand ONLY —
// never in the periodic scan. Missing/unreadable is not fatal: macOS/Linux
// accept token-less frames (spike-verified, see plan 附录 A), so callers
// degrade gracefully.
//
// (filepath.Glob returns sorted entries, so files[0] is deterministic;
// exactly one .key per pid in practice)
func loadClaudePeerToken(home string, pid int) string {
	if home = strings.TrimSpace(home); home == "" || pid <= 0 {
		return ""
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".*.key"))
	if err != nil || len(files) == 0 {
		return ""
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		return ""
	}
	var row struct {
		PeerToken string `json:"peerToken"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return ""
	}
	return strings.TrimSpace(row.PeerToken)
}

// 帧形态经 Task 0 spike 实测校准(计划附录 A):auth 帧字段名是 peerToken(mac 实测可省);
// user 帧实测被接受的形态带 msgV/msg_id/priority。
type ccPeerAuthFrame struct {
	Type      string `json:"type"`
	PeerToken string `json:"peerToken"`
}

type ccPeerUserFrame struct {
	MsgV    int    `json:"msgV"`
	MsgID   string `json:"msg_id"`
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Priority string `json:"priority"`
}

// ccPeerMsgID mints a uuid4-shaped id for the user frame. Entropy failure
// falls back to a timestamp id — never fail the notice over id randomness.
func ccPeerMsgID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ccPeerAuthLine(token string) string {
	b, _ := json.Marshal(ccPeerAuthFrame{Type: "auth", PeerToken: token})
	return string(b)
}

func ccPeerUserLine(content string) string {
	var f ccPeerUserFrame
	f.MsgV = 1
	f.MsgID = ccPeerMsgID()
	f.Type = "user"
	f.Message.Role = "user"
	f.Message.Content = content
	f.Priority = "next"
	b, _ := json.Marshal(f)
	return string(b)
}

// ccPeerInject dials the inbox UDS and writes the auth (when known) + user
// frames, fire-and-forget. Spike-verified (Task 0, plan 附录 A): a normal
// delivery produces NO receipt frame on the injecting socket, so there is
// nothing to read — success means the frames flushed. Non-delivery outcomes
// surface elsewhere: a busy TUI queues the message (delivered after its
// turn); a held/denied inbound gate shows its approval prompt inside the TUI.
func ccPeerInject(socketPath, token, digest string) error {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	w := bufio.NewWriter(conn)
	if token != "" {
		if _, err := w.WriteString(ccPeerAuthLine(token) + "\n"); err != nil {
			return err
		}
	}
	if _, err := w.WriteString(ccPeerUserLine(digest) + "\n"); err != nil {
		return err
	}
	return w.Flush()
}
