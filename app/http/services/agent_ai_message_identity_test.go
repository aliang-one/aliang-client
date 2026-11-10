package services

// 用户发言的 message_id 兜底纪律（2026-11-10 聊天记录身份化）。
//
// 缺陷原形：ai.message / ai.steer 在客户端没带 message_id 时回落
// messageID = sessionID。journal 的去重键是 sessionID + role + messageID，
// 于是同一会话里所有「缺 id」的发言共用同一个键——第二条起全部命中去重被
// 静默丢弃。用户侧看到的就是「手机上问的，网页上看不见」。
//
// 修法：缺 id 时现场铸造唯一 id（不硬拒，旧客户端继续可用），并且在
// ai.message.received / ai.steer ack 里回传，客户端可据以对账。本文件锁死
// ①解析函数的两条分支；②铸造 id 的唯一性与形态；③journal 层面的因果
// （同一 id 仍去重、不同 id 各落一行）——即缺陷不再复发。

import (
	"strings"
	"testing"
)

func TestResolveAgentAIUserMessageIDKeepsClientValue(t *testing.T) {
	for _, in := range []string{"msg_abc", "  msg_padded  ", "anything-goes"} {
		got, minted, err := resolveAgentAIUserMessageID(in)
		if err != nil {
			t.Fatalf("resolve(%q) 出错: %v", in, err)
		}
		if minted {
			t.Errorf("客户端已带 id 时不得走铸造分支: %q", in)
		}
		if want := strings.TrimSpace(in); got != want {
			t.Errorf("resolve(%q) = %q, want %q（只做 trim，不改写）", in, got, want)
		}
	}
}

func TestResolveAgentAIUserMessageIDMintsWhenAbsent(t *testing.T) {
	var seen []string
	for _, in := range []string{"", "   ", "\t\n"} {
		got, minted, err := resolveAgentAIUserMessageID(in)
		if err != nil {
			t.Fatalf("resolve(%q) 出错: %v", in, err)
		}
		if !minted {
			t.Fatalf("空 id 必须走铸造分支: %q", in)
		}
		if !strings.HasPrefix(got, "msg_") {
			t.Errorf("铸造 id 形态 = %q, want msg_ 前缀", got)
		}
		if len(got) <= len("msg_") {
			t.Errorf("铸造 id 过短: %q", got)
		}
		seen = append(seen, got)
	}
	if len(seen) != 3 {
		t.Fatalf("铸造次数 = %d, want 3", len(seen))
	}
	uniq := map[string]struct{}{}
	for _, id := range seen {
		uniq[id] = struct{}{}
	}
	// 唯一性是整个修复的地基：若两次铸造可能撞号，去重键重新变成共享键，
	// 「缺 id 的发言互相吞掉」这个缺陷原样复活。
	if len(uniq) != len(seen) {
		t.Fatalf("铸造 id 必须互不相同，实际 %v", seen)
	}
}

// TestAgentCoreJournalIdlessTurnsNoLongerCollide 是本修复的因果证明：
// 同一会话内两条「客户端没带 id」的发言，经兜底铸造后各自落一行；
// 而显式重复的同一 id 仍然去重（幂等契约没被削弱）。
func TestAgentCoreJournalIdlessTurnsNoLongerCollide(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	const sid = "s-idless"

	// 模拟旧行为：两次都拿到 sessionID 当 id → 必须只剩一行（缺陷现场）。
	j.appendUser(sid, sid, "第一问", "2026-11-10T00:00:01Z")
	j.appendUser(sid, sid, "第二问", "2026-11-10T00:00:02Z")
	if rows := journalUserRows(t, dir, sid); len(rows) != 1 {
		t.Fatalf("回落 sessionID 时应只剩 1 行（这就是缺陷原形），实际 %d: %v", len(rows), rows)
	}

	// 修复后：两次都缺 id，经 resolve 铸造出不同 id → 两行都在。
	dir2 := t.TempDir()
	j2 := newAgentCoreJournal(dir2)
	const sid2 = "s-idless-fixed"
	for _, text := range []string{"第一问", "第二问"} {
		id, minted, err := resolveAgentAIUserMessageID("")
		if err != nil || !minted {
			t.Fatalf("铸造失败: %v", err)
		}
		j2.appendUser(sid2, id, text, "2026-11-10T00:00:01Z")
	}
	rows := journalUserRows(t, dir2, sid2)
	if len(rows) != 2 {
		t.Fatalf("铸造后两条发言必须各落一行，实际 %d: %v", len(rows), rows)
	}
	for i, want := range []string{"第一问", "第二问"} {
		if got, _ := rows[i]["text"].(string); got != want {
			t.Errorf("第 %d 行 text = %q, want %q", i, got, want)
		}
	}

	// 显式重复 id 仍去重（重连重发的幂等契约不变）。
	j2.appendUser(sid2, "dup-1", "重复", "2026-11-10T00:00:03Z")
	j2.appendUser(sid2, "dup-1", "重复", "2026-11-10T00:00:03Z")
	if rows := journalUserRows(t, dir2, sid2); len(rows) != 3 {
		t.Fatalf("同一显式 id 重复写必须去重，实际 %d 行: %v", len(rows), rows)
	}
}

func journalUserRows(t *testing.T, dir, sessionID string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, row := range readJournalLines(t, dir, sessionID) {
		if row["kind"] == "msg" && row["role"] == "user" {
			out = append(out, row)
		}
	}
	return out
}
