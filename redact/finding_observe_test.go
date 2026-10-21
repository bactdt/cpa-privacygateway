package redact

import (
	"strings"
	"testing"
)

func newObservedMapping(t *testing.T) *Mapping {
	t.Helper()
	m, err := NewMapping(100, 1<<20)
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	return m
}

// normalizeNonce 把输出中的占位符 nonce 归一化，使两次独立调用的结果可比较
// （每次 NewMapping 生成随机 nonce，逐字节守恒等价于归一化后逐字节相同）。
func normalizeNonce(s string) string {
	return placeholderRe.ReplaceAllString(s, "[[PG_NONCE]]")
}

// TestRedactObservedByteIdentical 验证需求 4.4：sink 非 nil 时输出与 Redact 结构等价。
func TestRedactObservedByteIdentical(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"我的邮箱是 user@example.com，电话 13812345678"}]}`)
	cfg := DefaultConfig()

	m1 := newObservedMapping(t)
	plain, n1, err := m1.Redact(string(body), 0, cfg)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	_ = n1

	m2 := newObservedMapping(t)
	var findings []Finding
	observed, n2, err := m2.RedactObserved(string(body), 0, cfg, []string{"messages", "0", "content"}, func(f Finding) {
		findings = append(findings, f)
	})
	if err != nil {
		t.Fatalf("RedactObserved: %v", err)
	}
	if n1 != n2 || normalizeNonce(plain) != normalizeNonce(observed) {
		t.Errorf("输出不一致：plain=%q observed=%q", plain, observed)
	}
	if len(findings) != 2 {
		t.Fatalf("应有 2 个 Finding，实际 %d", len(findings))
	}
	if findings[0].Preview == "" || findings[1].Preview == "" {
		t.Error("Preview 不应为空")
	}
	if strings.Contains(findings[0].Preview, "user@example.com") || strings.Contains(findings[1].Preview, "13812345678") {
		t.Error("Finding 不得含原值")
	}
}

// TestRedactObservedReused 验证需求 4.3：重复值 Reused=true。
func TestRedactObservedReused(t *testing.T) {
	m := newObservedMapping(t)
	cfg := DefaultConfig()
	var findings []Finding
	text := "user@example.com 与 user@example.com"
	_, _, err := m.RedactObserved(text, 0, cfg, nil, func(f Finding) { findings = append(findings, f) })
	if err != nil {
		t.Fatalf("RedactObserved: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("应有 2 个 Finding，实际 %d", len(findings))
	}
	if findings[0].Reused {
		t.Error("首次检出 Reused 应为 false")
	}
	if !findings[1].Reused {
		t.Error("重复值 Reused 应为 true")
	}
}

// TestRedactObservedSinkPanic 验证需求 4.5：sink panic 不影响输出。
func TestRedactObservedSinkPanic(t *testing.T) {
	m := newObservedMapping(t)
	cfg := DefaultConfig()
	out, n, err := m.RedactObserved("user@example.com", 0, cfg, nil, func(Finding) { panic("boom") })
	if err != nil {
		t.Fatalf("panic 不应影响返回: %v", err)
	}
	if n != 1 || !strings.Contains(out, "[[PG_") {
		t.Errorf("panic 后输出应正常: n=%d out=%q", n, out)
	}
}

// TestRedactJSONSinkNilByteIdentical 验证需求 4.4：WalkOptions.OnFinding=nil 时与接线前逐字节相同。
func TestRedactJSONSinkNilByteIdentical(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"邮箱 user@example.com"}]}`)
	cfg := DefaultConfig()

	m1 := newObservedMapping(t)
	plain, _, err := RedactJSON("openai", body, m1, cfg, WalkOptions{Format: "openai"})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}

	m2 := newObservedMapping(t)
	var findings []Finding
	observed, _, err := RedactJSON("openai", body, m2, cfg, WalkOptions{
		Format:    "openai",
		OnFinding: func(f Finding) { findings = append(findings, f) },
	})
	if err != nil {
		t.Fatalf("RedactJSON(observed): %v", err)
	}
	if string(plain) != string(observed) && normalizeNonce(string(plain)) != normalizeNonce(string(observed)) {
		t.Errorf("输出不一致：\nplain=   %s\nobserved=%s", plain, observed)
	}
	if len(findings) == 0 {
		t.Error("OnFinding 应被调用")
	}
	if findings[0].Path != "messages.[0].content" {
		t.Errorf("Path = %q，期望 messages.[0].content", findings[0].Path)
	}
}

// TestRestoreCounted 验证需求 4.6/4.7：输出与 Restore 相同，统计正确，样本上限 5。
func TestRestoreCounted(t *testing.T) {
	m := newObservedMapping(t)
	cfg := DefaultConfig()
	redacted, _, err := m.Redact("邮箱 user@example.com", 0, cfg)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}

	out, stats := m.RestoreCounted(redacted)
	if out != "邮箱 user@example.com" {
		t.Errorf("还原输出 = %q", out)
	}
	if stats.Restored != 1 || stats.Unknown != 0 {
		t.Errorf("stats = %+v", stats)
	}

	// 未知占位符：本 nonce 格式但序号不在映射中
	unknown := redacted + " 还有 [[PG_" + m.Nonce() + "_999]]"
	out2, stats2 := m.RestoreCounted(unknown)
	if stats2.Unknown != 1 || len(stats2.UnknownSamples) != 1 {
		t.Errorf("unknown stats = %+v", stats2)
	}
	if !strings.Contains(out2, "user@example.com") {
		t.Errorf("已知占位符仍应还原: %q", out2)
	}

	// Restore 输出与 RestoreCounted 输出逐字节相同
	if out3 := m.Restore(unknown); out3 != out2 {
		t.Errorf("Restore 与 RestoreCounted 输出不一致")
	}
}

// TestRestoreJSONCounted 验证需求 4.6/4.7：输出与 RestoreJSON 相同且统计正确。
func TestRestoreJSONCounted(t *testing.T) {
	m := newObservedMapping(t)
	cfg := DefaultConfig()
	body := []byte(`{"messages":[{"role":"user","content":"邮箱 user@example.com"}]}`)
	redacted, _, err := RedactJSON("openai", body, m, cfg, WalkOptions{Format: "openai"})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}

	out, stats, err := RestoreJSONCounted(redacted, m)
	if err != nil {
		t.Fatalf("RestoreJSONCounted: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("还原输出 = %q", out)
	}
	if stats.Restored != 1 || stats.Unknown != 0 {
		t.Errorf("stats = %+v", stats)
	}

	// 未知占位符（本 nonce 格式、不在映射中）
	injected := []byte(`{"x":"[[PG_` + m.Nonce() + `_999]]"}`)
	out2, stats2, err := RestoreJSONCounted(injected, m)
	if err != nil {
		t.Fatalf("RestoreJSONCounted(unknown): %v", err)
	}
	if stats2.Unknown != 1 || len(stats2.UnknownSamples) != 1 {
		t.Errorf("unknown stats = %+v", stats2)
	}
	if !strings.Contains(string(out2), "PG_") {
		t.Errorf("未知占位符应保留: %q", out2)
	}

	// 输出与 RestoreJSON 逐字节相同（同一 mapping 状态比较）
	plain, _, err := RestoreJSON(injected, m)
	if err != nil {
		t.Fatalf("RestoreJSON: %v", err)
	}
	if string(plain) != string(out2) {
		t.Errorf("RestoreJSON 与 RestoreJSONCounted 输出不一致:\n%s\n%s", plain, out2)
	}

	// 空映射早退
	empty := newObservedMapping(t)
	out3, stats3, err := RestoreJSONCounted([]byte(`{"x":"1"}`), empty)
	if err != nil || string(out3) != `{"x":"1"}` || stats3.Restored != 0 {
		t.Errorf("空映射: out=%s stats=%+v err=%v", out3, stats3, err)
	}
}
