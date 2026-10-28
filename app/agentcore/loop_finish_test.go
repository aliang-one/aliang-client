package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// decodeResultFrame 从 emit 出来的帧流里取出最后一条 result 帧。
func decodeResultFrame(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var result map[string]interface{}
	for sc.Scan() {
		var frame map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &frame); err != nil {
			t.Fatalf("帧不是合法 JSON: %v (原始: %s)", err, sc.Text())
		}
		if frame["type"] == "result" {
			result = frame
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取帧流失败: %v", err)
	}
	if result == nil {
		t.Fatal("没有发出 result 帧")
	}
	return result
}

// newTestRunner 返回一个 emit 写入 buf 的 runner，可单独驱动 finish。
func newTestRunner(buf *bytes.Buffer) *runner {
	return &runner{
		writer: newStreamWriter(func(b []byte) error {
			_, err := buf.Write(b)
			return err
		}, "sess-test"),
	}
}

// 撞回合上限时，宿主 ai.error 的正文（= result 帧的 result 字段）必须以
// 真因开头。旧行为把原因拼在模型最后一句话后面，手机错误卡片只渲染第一
// 段，用户看到的是「模型说要继续调研」而不是「达到最大回合数」。
func TestFinish_ErrorReasonComesFirst(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRunner(&buf)
	r.turnText.WriteString("找到了「待开始」的来源，接下来来看看 `isDraft` 是怎么判断的。")
	r.turns = 50 // 旧版回合上限的字面量;上限本体已移除,这里只测 finish 帧的文本拼装

	if _, err := r.finish(context.Background(), ResultSubtypeErrorMaxTurns, "达到单次运行的最大回合数 50，已停止", true, nil); err != nil {
		t.Fatalf("finish 返回错误: %v", err)
	}

	frame := decodeResultFrame(t, buf.Bytes())
	got, _ := frame["result"].(string)
	want := "达到单次运行的最大回合数 50，已停止\n\n找到了「待开始」的来源，接下来来看看 `isDraft` 是怎么判断的。"
	if got != want {
		t.Fatalf("result 文本不对\n 实际: %q\n 期望: %q", got, want)
	}
	if frame["is_error"] != true {
		t.Fatalf("is_error 应为 true，实际 %v", frame["is_error"])
	}
	if frame["subtype"] != ResultSubtypeErrorMaxTurns {
		t.Fatalf("subtype 应为 %q，实际 %v", ResultSubtypeErrorMaxTurns, frame["subtype"])
	}
}

// 没有回合文本时只发原因本身，不能多出前缀空行。
func TestFinish_ErrorReasonOnlyNoPadding(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRunner(&buf)

	if _, err := r.finish(context.Background(), ResultSubtypeErrorRun, "上游返回 500", true, nil); err != nil {
		t.Fatalf("finish 返回错误: %v", err)
	}

	frame := decodeResultFrame(t, buf.Bytes())
	got, _ := frame["result"].(string)
	if got != "上游返回 500" {
		t.Fatalf("result 文本应为纯原因，实际 %q", got)
	}
}

// 成功路径必须保持不变：result 仍是末回合文本，不加任何前缀。
// 宿主收尾去重（suffixNotStreamed）依赖这一点，改错会让整轮回复重刷。
func TestFinish_SuccessKeepsTurnTextOnly(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRunner(&buf)
	r.turnText.WriteString("这就是最终答案。")

	if _, err := r.finish(context.Background(), ResultSubtypeSuccess, "", false, nil); err != nil {
		t.Fatalf("finish 返回错误: %v", err)
	}

	frame := decodeResultFrame(t, buf.Bytes())
	got, _ := frame["result"].(string)
	if got != "这就是最终答案。" {
		t.Fatalf("成功路径 result 应只含末回合文本，实际 %q", got)
	}
	if frame["is_error"] != false {
		t.Fatalf("is_error 应为 false，实际 %v", frame["is_error"])
	}
}
