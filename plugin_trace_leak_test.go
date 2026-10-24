package main

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"duanjuapp/cpa/redact"
)

// 过期占位符（其他 nonce）在 trace 中只记录计数与掩码样例。
func TestTraceRedactDoneStale(t *testing.T) {
	p := newTraceTestPlugin(t)
	body := []byte(`{"messages":[{"role":"user","content":"old [[PG_0000000000000000_1]] mail user@example.com"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "r-stale", TraceID: "t-stale", SourceFormat: "openai", Body: body,
	})
	if err != nil || len(resp.Body) == 0 {
		t.Fatalf("before_auth: err=%v bodyLen=%d", err, len(resp.Body))
	}
	rec := p.traces.records["r-stale"]
	if rec == nil {
		t.Fatal("应有 trace 记录")
	}
	rd := rec.Events[1].Fields
	if rd["stale"] != 1 {
		t.Fatalf("stale = %v, fields=%v", rd["stale"], rd)
	}
	samples, _ := rd["stale_samples"].([]string)
	if len(samples) != 1 || samples[0] != "[[PG_0000…_1]]" {
		t.Fatalf("stale_samples = %v", samples)
	}
	if strings.Contains(dumpRecord(t, rec), "0000000000000000") {
		t.Fatal("trace 不应含完整 nonce")
	}
}

// trace 关闭时不安装 OnStale，也不计算样例。
func TestTraceStaleDisabled(t *testing.T) {
	m, err := redact.NewMapping(1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"old [[PG_0000000000000000_1]] mail user@example.com"}]}`)
	out, count, agg, _, err := redactObservedJSON("openai", body, m, redact.DefaultConfig(), defaultConfig(), traceLogger{})
	if err != nil || count == 0 || strings.Contains(string(out), "user@example.com") {
		t.Fatalf("redact: count=%d err=%v out=%s", count, err, out)
	}
	if agg.stale != 0 || len(agg.staleSamples) != 0 {
		t.Fatalf("trace 关闭仍聚合 stale: %d %v", agg.stale, agg.staleSamples)
	}
}

// stream.summary / stream.flush 带 source_format 与三类残留统计。
func TestTraceStreamLeakFields(t *testing.T) {
	t.Run("leftover_at_end", func(t *testing.T) {
		p := newTraceTestPlugin(t)
		body := []byte(`{"messages":[{"role":"user","content":"mail user@example.com"}]}`)
		if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
			RequestID: "r-stream", SourceFormat: "openai", Stream: true, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		chunk := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x [[PG_\"}}]}\n\n")
		if _, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
			RequestID: "r-stream", SourceFormat: "openai", ChunkIndex: 0, Body: chunk,
		}); err != nil {
			t.Fatal(err)
		}
		if err := p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
			RequestID: "r-stream", Outcome: pluginapi.RequestCompletionSucceeded, StatusCode: 200,
		}); err != nil {
			t.Fatal(err)
		}
		rec := p.traces.records["r-stream"]
		if rec == nil {
			t.Fatal("应有 trace 记录")
		}
		summary := fieldsOfStage(t, rec, "stream.summary")
		for _, key := range []string{"source_format", "unsupported_event", "missing_template", "leftover_at_end", "dangling_samples"} {
			if _, ok := summary[key]; !ok {
				t.Fatalf("stream.summary 缺字段 %q: %v", key, summary)
			}
		}
		if summary["source_format"] != "openai" {
			t.Fatalf("summary source_format = %v", summary["source_format"])
		}
		flush := fieldsOfStage(t, rec, "stream.flush")
		if flush["source_format"] != "openai" || flush["leftover_at_end"] != 1 {
			t.Fatalf("flush fields = %v", flush)
		}
		samples, _ := flush["dangling_samples"].([]string)
		if len(samples) == 0 || !strings.HasPrefix(samples[0], "[[PG_") {
			t.Fatalf("dangling_samples = %v", samples)
		}
	})

	t.Run("unsupported_event", func(t *testing.T) {
		p := newTraceTestPlugin(t)
		body := []byte(`{"input":[{"content":"mail user@example.com"}]}`)
		if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
			RequestID: "r-bare", SourceFormat: "openai-response", Stream: true, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
			RequestID: "r-bare", SourceFormat: "openai-response", ChunkIndex: 0,
			Body: []byte(`{"output_text":"x [[PG_"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if err := p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
			RequestID: "r-bare", Outcome: pluginapi.RequestCompletionSucceeded, StatusCode: 200,
		}); err != nil {
			t.Fatal(err)
		}
		rec := p.traces.records["r-bare"]
		summary := fieldsOfStage(t, rec, "stream.summary")
		if summary["source_format"] != "openai-response" || summary["unsupported_event"] != 1 {
			t.Fatalf("summary fields = %v", summary)
		}
		flush := fieldsOfStage(t, rec, "stream.flush")
		if flush["unsupported_event"] != 1 {
			t.Fatalf("flush fields = %v", flush)
		}
	})
}

// 遍历 trace 事件的全部字符串字段，断言不含任何原始敏感值。
func TestTraceFieldsNoRawValues(t *testing.T) {
	p := newTraceTestPlugin(t)
	raws := []string{"user@example.com", "13812345678", "sk-abcdefghijklmnop1234", "Summer2024abc"}
	body := []byte(`{"messages":[{"role":"user","content":"mail user@example.com phone 13812345678 key sk-abcdefghijklmnop1234 DB_PASSWORD=Summer2024abc"}]}`)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "r-raw", TraceID: "t-raw", SourceFormat: "openai", Stream: true, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		RequestID: "r-raw", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x [[PG_\"}}]}\n\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
		RequestID: "r-raw", TraceID: "t-raw", Outcome: pluginapi.RequestCompletionSucceeded, StatusCode: 200,
	}); err != nil {
		t.Fatal(err)
	}
	rec := p.traces.records["r-raw"]
	if rec == nil {
		t.Fatal("应有 trace 记录")
	}
	dump := dumpRecord(t, rec)
	for _, raw := range raws {
		if strings.Contains(dump, raw) {
			t.Fatalf("trace 含原值 %q: %s", raw, dump)
		}
	}
}

func fieldsOfStage(t *testing.T, rec *traceRecord, stage string) map[string]any {
	t.Helper()
	for i := len(rec.Events) - 1; i >= 0; i-- {
		if rec.Events[i].Stage == stage {
			return rec.Events[i].Fields
		}
	}
	t.Fatalf("缺少 stage %q: %v", stage, stagesOf(rec))
	return nil
}
