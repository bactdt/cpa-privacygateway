package main

import (
	"strings"
	"testing"
	"time"

	"duanjuapp/cpa/redact"
)

func enabledBuffer(maxRequests, maxEvents int) *traceBuffer {
	b := newTraceBuffer()
	b.configure(true, maxRequests, maxEvents)
	return b
}

// 需求 6.2/6.3：满员淘汰最旧；被淘汰后重建的请求标记 partial。
func TestTraceRingEvictionAndPartial(t *testing.T) {
	b := enabledBuffer(2, 8)
	b.record("r1", "t1", "request.received", nil)
	b.record("r2", "t2", "request.received", nil)
	b.record("r3", "t3", "request.received", nil) // 淘汰 r1

	if _, ok := b.records["r1"]; ok {
		t.Error("r1 应被淘汰")
	}
	if b.evicted != 1 {
		t.Errorf("evicted = %d", b.evicted)
	}
	// r1 再次出现 → 淘汰 r2、新建 r1 且 partial
	b.record("r1", "t1", "request.received", nil)
	if rec := b.records["r1"]; rec == nil || !rec.Partial {
		t.Errorf("重建记录应 partial: %+v", b.records["r1"])
	}
	// 未被淘汰的 r3 不 partial；r2 已被挤出
	if b.records["r3"].Partial {
		t.Error("r3 不应 partial")
	}
	if _, ok := b.records["r2"]; ok {
		t.Error("r2 应被挤出")
	}
}

// 需求 6.4：事件达 maxEvents-1 后丢弃非结局事件并计数，request.complete 保留。
func TestTraceEventCapKeepsComplete(t *testing.T) {
	b := enabledBuffer(4, 3)
	b.record("r1", "", "request.received", nil)
	b.record("r1", "", "redact.done", map[string]any{"findings": 2}) // 达 2 = maxEvents-1
	b.record("r1", "", "response.restored", nil)                      // 应丢弃
	b.record("r1", "", "request.complete", map[string]any{"outcome": "succeeded"})

	rec := b.records["r1"]
	if len(rec.Events) != 3 {
		t.Fatalf("事件数 = %d，期望 3", len(rec.Events))
	}
	if rec.Events[2].Stage != "request.complete" {
		t.Errorf("末事件 = %s，应为 request.complete", rec.Events[2].Stage)
	}
	if rec.DroppedEvents != 1 {
		t.Errorf("dropped_events = %d", rec.DroppedEvents)
	}
	if rec.Outcome != "succeeded" {
		t.Errorf("outcome = %q", rec.Outcome)
	}
}

// 需求 6.8：空 RequestID 合成 anon 键并标记 anonymous。
func TestTraceAnonymousKey(t *testing.T) {
	b := enabledBuffer(4, 8)
	b.record("", "t9", "request.received", nil)
	b.record("", "t9", "request.received", nil)
	if len(b.records) != 2 {
		t.Errorf("每次空 ID 应独立合成键，记录数 = %d", len(b.records))
	}
	for key, rec := range b.records {
		if !strings.HasPrefix(key, "anon-") || !rec.Anonymous {
			t.Errorf("%s: %+v", key, rec.Anonymous)
		}
	}
}

// 需求 7.4：关闭清空；7.3：缩容淘汰最旧。
func TestTraceConfigureDisableAndShrink(t *testing.T) {
	b := enabledBuffer(4, 8)
	b.record("r1", "", "request.received", nil)
	b.record("r2", "", "request.received", nil)
	b.record("r3", "", "request.received", nil)
	b.configure(false, 4, 8)
	if len(b.records) != 0 {
		t.Errorf("关闭应清空，记录数 = %d", len(b.records))
	}
	b.record("x", "", "request.received", nil)
	if len(b.records) != 0 {
		t.Error("关闭态 record 应为 no-op")
	}

	b.configure(true, 4, 8)
	b.record("a", "", "request.received", nil)
	b.record("c", "", "request.received", nil)
	b.configure(true, 1, 8) // 缩容 → 只留最后创建的 c？按首次出现淘汰最旧 → 淘汰 a
	if _, ok := b.records["a"]; ok {
		t.Error("缩容应淘汰最旧的 a")
	}
	if _, ok := b.records["c"]; !ok {
		t.Error("c 应保留")
	}
}

// 需求 11：since/limit/filter 语义与 next_since 单调性。
func TestTraceSnapshot(t *testing.T) {
	b := enabledBuffer(8, 8)
	b.record("r1", "", "request.received", nil)
	b.record("r2", "", "redact.done", map[string]any{"findings": 1})
	b.record("r3", "", "request.rejected", map[string]any{"status": 429})

	snap := b.snapshot(0, 10, "")
	if len(snap.Requests) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(snap.Requests))
	}
	// first_seen 倒序 → r3 在最前
	if snap.Requests[0].RequestID != "r3" || snap.Requests[2].RequestID != "r1" {
		t.Errorf("排序错误: %s %s %s", snap.Requests[0].RequestID, snap.Requests[1].RequestID, snap.Requests[2].RequestID)
	}
	if snap.Buffered != 3 || snap.NextSince != b.seq {
		t.Errorf("buffered=%d next_since=%d seq=%d", snap.Buffered, snap.NextSince, b.seq)
	}

	// findings 过滤
	onlyFindings := b.snapshot(0, 10, "findings")
	if len(onlyFindings.Requests) != 1 || onlyFindings.Requests[0].RequestID != "r2" {
		t.Errorf("findings 过滤错误: %+v", onlyFindings.Requests)
	}
	// errors 过滤（terminated）
	onlyErrors := b.snapshot(0, 10, "errors")
	if len(onlyErrors.Requests) != 1 || onlyErrors.Requests[0].RequestID != "r3" {
		t.Errorf("errors 过滤错误: %+v", onlyErrors.Requests)
	}
	// since 增量：取最旧记录的 seq，应只剩较新的 2 条
	since := b.snapshot(0, 10, "").Requests[2].UpdatedSeq
	incremental := b.snapshot(since, 10, "")
	if len(incremental.Requests) != 2 {
		t.Errorf("since 后应剩 2 条增量，实际 %d", len(incremental.Requests))
	}
	// since 取最新记录的 seq → 无增量
	newest := b.snapshot(0, 10, "").Requests[0].UpdatedSeq
	if got := b.snapshot(newest, 10, ""); len(got.Requests) != 0 || got.NextSince < newest {
		t.Errorf("since=最新 seq 后应无增量: %d", len(got.Requests))
	}
	// limit
	limited := b.snapshot(0, 2, "")
	if len(limited.Requests) != 2 {
		t.Errorf("limit=2 实际 %d", len(limited.Requests))
	}
}

// 需求 11.5：关闭态返回空列表且 trace_enabled=false。
func TestSnapshotDisabled(t *testing.T) {
	b := newTraceBuffer()
	snap := b.snapshot(0, 10, "")
	if snap.Enabled || len(snap.Requests) != 0 {
		t.Errorf("关闭态: %+v", snap)
	}
}

// TD-6：stream.summary 每块原地更新，不增长事件数。
func TestTraceStreamSummaryInPlace(t *testing.T) {
	b := enabledBuffer(4, 8)
	b.record("r1", "", "request.received", nil)
	b.recordLatest("r1", "", "stream.summary", map[string]any{"chunks": 1})
	b.recordLatest("r1", "", "stream.summary", map[string]any{"chunks": 5})
	b.recordLatest("r1", "", "redact.done", nil) // 非同 stage → 追加
	b.recordLatest("r1", "", "stream.summary", map[string]any{"chunks": 9}) // 末尾不是 summary → 追加

	rec := b.records["r1"]
	if len(rec.Events) != 4 {
		t.Fatalf("事件数 = %d，期望 4（summary 两次原地更新）", len(rec.Events))
	}
	if rec.Events[1].Fields["chunks"] != 5 {
		t.Errorf("原地更新失败: %+v", rec.Events[1].Fields)
	}
}

// 需求 6.7：事件 at 单调不减。
func TestTraceMonotonicAt(t *testing.T) {
	b := enabledBuffer(4, 8)
	b.record("r1", "", "request.received", nil)
	first := b.records["r1"].Events[0].At
	for i := 0; i < 3; i++ {
		b.record("r1", "", "response.restored", nil)
	}
	evs := b.records["r1"].Events
	for i := 1; i < len(evs); i++ {
		if evs[i].At.Before(evs[i-1].At) {
			t.Errorf("at 回退: %v < %v", evs[i].At, evs[i-1].At)
		}
	}
	if evs[0].At.Before(first) {
		t.Error("时间倒退")
	}
}

// 需求 18：redact.done 汇总字段累加（findings/counts/truncated）。
func TestTraceRedactDoneSummary(t *testing.T) {
	b := enabledBuffer(4, 16)
	b.record("r1", "", "redact.done", map[string]any{
		"findings": 2, "counts": map[string]int{"email": 2}, "findings_truncated": 1,
	})
	b.record("r1", "", "redact.done", map[string]any{
		"findings": 3, "counts": map[string]int{"phone": 3},
	})
	rec := b.records["r1"]
	if rec.FindingTotal != 5 || rec.Counts["email"] != 2 || rec.Counts["phone"] != 3 {
		t.Errorf("汇总错误: total=%d counts=%v", rec.FindingTotal, rec.Counts)
	}
	if rec.FindingsTruncated != 1 {
		t.Errorf("truncated = %d", rec.FindingsTruncated)
	}
}

// 需求 11.10：clear 返回清空前记录数并清空。
func TestTraceClear(t *testing.T) {
	b := enabledBuffer(4, 8)
	b.record("r1", "", "request.received", nil)
	b.record("r2", "", "request.received", nil)
	if n := b.clear(); n != 2 {
		t.Errorf("clear 返回 %d", n)
	}
	if len(b.records) != 0 {
		t.Error("clear 后应为空")
	}
}

// 需求 7.5：关闭态 traceLogger 全部 no-op。
func TestTraceLoggerDisabledNoOp(t *testing.T) {
	cfg := defaultConfig()
	traceOff := false
	cfg.TraceEnabled = &traceOff
	p := newPlugin(cfg)
	if p.traces == nil {
		t.Fatal("plugin 应持有 traceBuffer")
	}
	if p.traces.traceOn() {
		t.Fatal("trace_enabled=false 时缓冲应关闭")
	}
	tl := p.trace("r1", "t1")
	if tl.on() {
		t.Error("关闭态 trace() 应返回空句柄")
	}
	tl.received("before_auth", 0, "openai", "claude", "m", "m", true, 100)
	tl.rejected("concurrency", 429)
	tl.blocked("reject_keyword", 403, 10, 5)
	tl.passthrough("body_too_large")
	agg := newFindingAgg()
	agg.sink(redact.Finding{Kind: redact.KindEmail, Placeholder: "[[PG_1]]", Preview: "u***@e.com", Path: "messages.[0].content"})
	tl.redactDone(agg, 1, time.Millisecond, false)
	tl.redactFailed("capacity", "terminate", 500)
	tl.responseRestored(200, 10, 1, 0, nil, "")
	tl.streamSummary(1, 1, 0, 0, 0)
	tl.streamFlush(1, 5, 0)
	tl.pluginError("panic", "m", "none")
	tl.complete("succeeded", 200, false, time.Now(), time.Now())
	if len(p.traces.records) != 0 {
		t.Error("关闭态不应产生记录")
	}
}

// 需求 6.6：findingAgg 明细上限 64、超出计 truncated、counts 按 Kind 聚合。
func TestFindingAgg(t *testing.T) {
	agg := newFindingAgg()
	for i := 0; i < 70; i++ {
		agg.sink(redact.Finding{Kind: redact.KindEmail, Placeholder: "[[PG]]", Preview: "p", Path: "x"})
	}
	if agg.total != 70 || len(agg.details) != 64 || agg.truncated != 6 {
		t.Errorf("total=%d details=%d truncated=%d", agg.total, len(agg.details), agg.truncated)
	}
	if agg.counts["email"] != 70 {
		t.Errorf("counts = %v", agg.counts)
	}
}

// 需求 5：recordABIFailure 只解码两个字段并记录 plugin.error。
func TestRecordABIFailure(t *testing.T) {
	b := enabledBuffer(4, 8)
	recordABIFailure(b, "some.method", `{"request_id":"r7","trace_id":"t7","model":"secret-model"}`, "decode_failed")
	rec := b.records["r7"]
	if rec == nil {
		t.Fatal("应创建 r7 记录")
	}
	if rec.TraceID != "t7" || len(rec.Events) != 1 {
		t.Errorf("trace_id=%q events=%d", rec.TraceID, len(rec.Events))
	}
	f := rec.Events[0].Fields
	if f["method"] != "some.method" || f["reason"] != "decode_failed" {
		t.Errorf("fields = %v", f)
	}
	if _, leaked := f["model"]; leaked {
		t.Error("不得解码 model 字段")
	}
	// 关闭态 no-op
	b2 := newTraceBuffer()
	recordABIFailure(b2, "m", `{"request_id":"x"}`, "panic")
	if len(b2.records) != 0 {
		t.Error("关闭态应 no-op")
	}
	recordABIFailure(nil, "m", `{}`, "panic") // nil buffer 不得 panic
}
