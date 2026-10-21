package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func newTraceTestPlugin(t *testing.T) *plugin {
	t.Helper()
	p := newPlugin(defaultConfig())
	p.traces.configure(true, 16, 32)
	p.traces.setRecording(true)
	return p
}

func stagesOf(rec *traceRecord) []string {
	out := make([]string, 0, len(rec.Events))
	for _, ev := range rec.Events {
		out = append(out, ev.Stage)
	}
	return out
}

func dumpRecord(t *testing.T, rec *traceRecord) string {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// RT-1 全链路：request.received → redact.done → response.restored → request.complete，
// 且入库内容绝不含原邮箱（需求 3/5）。
func TestTraceWiringFullLifecycle(t *testing.T) {
	p := newTraceTestPlugin(t)
	ctx := context.Background()
	body := []byte(`{"messages":[{"role":"user","content":"我的邮箱 user@example.com"}]}`)

	resp, err := p.InterceptRequestBeforeAuth(ctx, pluginapi.RequestInterceptRequest{
		RequestID: "r1", TraceID: "t1", SourceFormat: "openai",
		ToFormat: "claude", Model: "m", RequestedModel: "m", Stream: true, Body: body,
	})
	if err != nil || len(resp.Body) == 0 {
		t.Fatalf("before_auth: err=%v bodyLen=%d", err, len(resp.Body))
	}
	if strings.Contains(string(resp.Body), "user@example.com") {
		t.Fatal("正文未脱敏")
	}

	rec := p.traces.records["r1"]
	if rec == nil {
		t.Fatal("应有 r1 记录")
	}
	if rec.TraceID != "t1" {
		t.Errorf("trace_id = %q", rec.TraceID)
	}
	stages := stagesOf(rec)
	if len(stages) != 2 || stages[0] != "request.received" || stages[1] != "redact.done" {
		t.Fatalf("stages = %v", stages)
	}
	rx := rec.Events[0].Fields
	if rx["hook"] != "before_auth" || rx["stream"] != true || rx["body_bytes"] != len(body) {
		t.Errorf("received fields = %v", rx)
	}
	rd := rec.Events[1].Fields
	if rd["findings"] != 1 || rd["stateless"] != false {
		t.Errorf("redact.done fields = %v", rd)
	}
	details, _ := rd["details"].([]map[string]any)
	if len(details) != 1 {
		t.Fatalf("details = %v", rd["details"])
	}
	preview, _ := details[0]["preview"].(string)
	if preview == "" || strings.Contains(preview, "user@example.com") || !strings.Contains(preview, "example.com") {
		t.Errorf("preview = %q", preview)
	}
	if details[0]["path"] != "messages.[0].content" {
		t.Errorf("path = %v", details[0]["path"])
	}
	if rec.FindingTotal != 1 || rec.Counts["email"] != 1 {
		t.Errorf("summary: total=%d counts=%v", rec.FindingTotal, rec.Counts)
	}

	if _, err := p.InterceptResponse(ctx, pluginapi.ResponseInterceptRequest{
		RequestID: "r1", StatusCode: 200, Body: resp.Body,
	}); err != nil {
		t.Fatalf("InterceptResponse: %v", err)
	}
	if err := p.HandleRequestComplete(ctx, pluginapi.RequestCompletion{
		RequestID: "r1", TraceID: "t1", Outcome: pluginapi.RequestCompletionSucceeded,
		StatusCode: 200,
	}); err != nil {
		t.Fatalf("HandleRequestComplete: %v", err)
	}
	rec = p.traces.records["r1"]
	stages = stagesOf(rec)
	want := []string{"request.received", "redact.done", "response.restored", "request.complete"}
	if len(stages) != len(want) {
		t.Fatalf("stages = %v", stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages = %v", stages)
		}
	}
	rr := rec.Events[2].Fields
	if rr["restored"] != 1 || rr["skipped"] != "" {
		t.Errorf("restored fields = %v", rr)
	}
	if rec.Outcome != "succeeded" || rec.Status != 200 {
		t.Errorf("outcome=%q status=%d", rec.Outcome, rec.Status)
	}

	// 整个缓冲序列化后不含原值（需求 5）
	for _, r := range p.traces.records {
		if strings.Contains(dumpRecord(t, r), "user@example.com") {
			t.Errorf("记录含原值: %s", dumpRecord(t, r))
		}
	}
}

// 关键词拦截 → guard.blocked，且不含关键词本身（需求 5/TD-CPA-018）。
func TestTraceWiringGuardBlocked(t *testing.T) {
	cfg := defaultConfig()
	cfg.RejectKeywords = []string{"BLOCKME"}
	p := newPlugin(cfg)
	p.traces.configure(true, 8, 16)
	p.traces.setRecording(true)

	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "r2", SourceFormat: "openai", Body: []byte(`{"messages":[{"role":"user","content":"BLOCKME now"}]}`),
	})
	if err != nil || !resp.Terminate || resp.StatusCode != 403 {
		t.Fatalf("应 403 终止: %+v err=%v", resp, err)
	}
	rec := p.traces.records["r2"]
	if rec == nil || !rec.Terminated || rec.Status != 403 {
		t.Fatalf("记录: %+v", rec)
	}
	var blocked map[string]any
	for _, ev := range rec.Events {
		if ev.Stage == "guard.blocked" {
			blocked = ev.Fields
		}
	}
	if blocked == nil || blocked["reason"] != "reject_keyword" {
		t.Fatalf("guard.blocked = %v", blocked)
	}
	if strings.Contains(dumpRecord(t, rec), "BLOCKME") {
		t.Errorf("记录泄露关键词: %s", dumpRecord(t, rec))
	}
}

// 并发上限 → request.rejected(concurrency_limit, 429)。
func TestTraceWiringConcurrencyRejected(t *testing.T) {
	cfg := concurrencyConfig()
	cfg.MaxConcurrency = 1
	p := newPlugin(cfg)
	p.traces.configure(true, 8, 16)
	p.traces.setRecording(true)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "r1", SourceFormat: "openai", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "r2", SourceFormat: "openai", Body: body,
	})
	if err != nil || !resp.Terminate || resp.StatusCode != 429 {
		t.Fatalf("应 429: %+v err=%v", resp, err)
	}
	rec := p.traces.records["r2"]
	if rec == nil || !rec.Terminated || rec.Status != 429 {
		t.Fatalf("记录: %+v", rec)
	}
	found := false
	for _, ev := range rec.Events {
		if ev.Stage == "request.rejected" && ev.Fields["reason"] == "concurrency_limit" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 request.rejected: %v", stagesOf(rec))
	}
}

// after_auth 尝试号（需求 18：attempt）。
func TestTraceWiringAfterAuthAttempt(t *testing.T) {
	p := newTraceTestPlugin(t)
	body := []byte(`{"messages":[{"role":"user","content":"邮箱 user@example.com"}]}`)
	if _, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "a1", SourceFormat: "openai", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "a1", SourceFormat: "openai", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	rec := p.traces.records["a1"]
	if rec == nil {
		t.Fatal("缺记录")
	}
	var gotAttempts []any
	for _, ev := range rec.Events {
		if ev.Stage == "request.received" {
			gotAttempts = append(gotAttempts, ev.Fields["attempt"])
			if ev.Fields["hook"] != "after_auth" {
				t.Errorf("hook = %v", ev.Fields["hook"])
			}
		}
	}
	if len(gotAttempts) != 2 || gotAttempts[0] != 1 || gotAttempts[1] != 2 {
		t.Errorf("attempts = %v", gotAttempts)
	}
}

// 流式：stream.summary 原地更新 + stream.flush。
func TestTraceWiringStream(t *testing.T) {
	p := newTraceTestPlugin(t)
	ctx := context.Background()
	body := []byte(`{"messages":[{"role":"user","content":"邮箱 user@example.com"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(ctx, pluginapi.RequestInterceptRequest{
		RequestID: "s1", SourceFormat: "openai", Body: body, Stream: true,
	})
	if err != nil || len(resp.Body) == 0 {
		t.Fatalf("before_auth: %v", err)
	}
	ph := firstPlaceholder(t, resp.Body)
	chunk := []byte(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", ph))
	for i := 0; i < 2; i++ {
		if _, err := p.InterceptStreamChunk(ctx, pluginapi.StreamChunkInterceptRequest{
			RequestID: "s1", SourceFormat: "openai", Body: chunk, ChunkIndex: i,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.HandleRequestComplete(ctx, pluginapi.RequestCompletion{
		RequestID: "s1", Outcome: pluginapi.RequestCompletionSucceeded, StatusCode: 200,
	}); err != nil {
		t.Fatal(err)
	}
	rec := p.traces.records["s1"]
	if rec == nil {
		t.Fatal("缺记录")
	}
	var summaries, flushes int
	for _, ev := range rec.Events {
		switch ev.Stage {
		case "stream.summary":
			summaries++
			if ev.Fields["chunks"] != 2 {
				t.Errorf("chunks = %v（应原地更新为 2）", ev.Fields["chunks"])
			}
		case "stream.flush":
			flushes++
		}
	}
	if summaries != 1 || flushes != 1 {
		t.Errorf("summary=%d flush=%d stages=%v", summaries, flushes, stagesOf(rec))
	}
}

// firstPlaceholder 从脱敏正文中取出第一个完整占位符。
func firstPlaceholder(t *testing.T, body []byte) string {
	t.Helper()
	s := string(body)
	i := strings.Index(s, "[[PG_")
	if i < 0 {
		t.Fatalf("正文无占位符: %s", s)
	}
	j := strings.Index(s[i:], "]]")
	if j < 0 {
		t.Fatalf("占位符未闭合: %s", s[i:])
	}
	return s[i : i+j+2]
}
