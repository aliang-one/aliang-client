package services

import (
	"bytes"
	"testing"
)

func TestTerminalRingBufferKeepsTail(t *testing.T) {
	r := newTerminalRingBuffer(10)
	r.push([]byte("abcdefgh")) // 8，整块 1 个
	r.push([]byte("ijklm"))    // +5 → 总 13 > 10，整块淘汰第一个 chunk（Board 同款，不做字节级裁切）
	got := r.snapshot()
	if want := "ijklm"; !bytes.Equal(got, []byte(want)) {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
}

func TestTerminalRingBufferOversizedChunkKeepsTail(t *testing.T) {
	r := newTerminalRingBuffer(4)
	r.push([]byte("abcdef")) // 单块超限 → 截尾
	if got, want := string(r.snapshot()), "cdef"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
}

func TestTerminalRingBufferEmptyAndFloor(t *testing.T) {
	r := newTerminalRingBuffer(0) // 低于 64KB 地板 → 提到地板
	if r.max != 64*1024 {
		t.Fatalf("max = %d, want floor 64KiB", r.max)
	}
	if len(r.snapshot()) != 0 {
		t.Fatal("empty ring snapshot must be empty")
	}
}

func TestTerminalRingBufferConcurrentPushSnapshot(t *testing.T) {
	r := newTerminalRingBuffer(1 << 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			r.push([]byte("0123456789"))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = r.snapshot()
	}
	<-done
}
