package main

import (
	"encoding/json"
	"time"

	"duanjuapp/cpa/redact"
)

// traceLogger 是单请求的阶段记录句柄：所有方法在追踪关闭时立即返回（需求 7.5）。
type traceLogger struct {
	buf       *traceBuffer
	requestID string
	traceID   string
}

// trace 创建记录句柄；追踪未开启时 buf 为 nil，方法全部为 no-op。
func (p *plugin) trace(requestID, traceID string) traceLogger {
	if !p.traces.traceOn() {
		return traceLogger{}
	}
	return traceLogger{buf: p.traces, requestID: requestID, traceID: traceID}
}

func (t traceLogger) on() bool { return t.buf != nil }

// record 追加一条事件；关闭态 no-op。
func (t traceLogger) record(stage string, fields map[string]any) {
	if !t.on() {
		return
	}
	t.buf.record(t.requestID, t.traceID, stage, fields)
}

// recordLatest 同 record，但同 stage 末尾事件原地更新（stream.summary，TD-6）。
func (t traceLogger) recordLatest(stage string, fields map[string]any) {
	if !t.on() {
		return
	}
	t.buf.recordLatest(t.requestID, t.traceID, stage, fields)
}

// received 记录 request.received（需求 4/18 事件表）。
// hook 为 before_auth/after_auth；attempt 为 after_auth 第 n 次凭据尝试，0 表示省略。
func (t traceLogger) received(hook string, attempt int, sourceFormat, toFormat, model, requestedModel string, stream bool, bodyBytes int) {
	if !t.on() {
		return
	}
	fields := map[string]any{
		"hook":            hook,
		"source_format":   sourceFormat,
		"to_format":       toFormat,
		"model":           model,
		"requested_model": requestedModel,
		"stream":          stream,
		"body_bytes":      bodyBytes,
	}
	if attempt > 0 {
		fields["attempt"] = attempt
	}
	t.record("request.received", fields)
}

// rejected 记录 request.rejected（429/400/415 等终止，需求 4）。
func (t traceLogger) rejected(reason string, status int) {
	if !t.on() {
		return
	}
	t.record("request.rejected", map[string]any{"reason": reason, "status": status})
}

// blocked 记录 guard.blocked；绝不含命中的关键词本身（需求 5/TD-CPA-018）。
func (t traceLogger) blocked(reason string, status, bodyBytes, limit int) {
	if !t.on() {
		return
	}
	t.record("guard.blocked", map[string]any{
		"reason": reason, "status": status, "body_bytes": bodyBytes, "limit": limit,
	})
}

// passthrough 记录 guard.passthrough（fail_open 放行等）。
func (t traceLogger) passthrough(reason string) {
	if !t.on() {
		return
	}
	t.record("guard.passthrough", map[string]any{"reason": reason, "action": "passthrough"})
}

// redactDone 记录 redact.done；details 为掩码预览版 Finding（需求 4.1–4.3）。
// 超过 maxFindingDetails 的明细计入 findings_truncated（需求 6.6）。
func (t traceLogger) redactDone(agg *findingAgg, mappingSize int, elapsed time.Duration, stateless bool) {
	if !t.on() {
		return
	}
	t.record("redact.done", map[string]any{
		"findings":           agg.total,
		"unique":             len(agg.details),
		"counts":             agg.counts,
		"details":            agg.details,
		"findings_truncated": agg.truncated,
		"mapping_size":       mappingSize,
		"elapsed_us":         elapsed.Microseconds(),
		"stateless":          stateless,
		"stale":              agg.stale,
		"stale_samples":      agg.staleSamples,
	})
}

// redactFailed 记录 redact.failed；reason 为错误分类码，不记错误原文（需求 5）。
func (t traceLogger) redactFailed(reason, action string, status int) {
	if !t.on() {
		return
	}
	t.record("redact.failed", map[string]any{"reason": reason, "action": action, "status": status})
}

// responseRestored 记录 response.restored；skipped 为 no_mapping/empty_body/parse_failed，正常时为空串。
func (t traceLogger) responseRestored(statusCode, bodyBytes, restored, unknown int, unknownSamples []string, skipped string) {
	if !t.on() {
		return
	}
	t.record("response.restored", map[string]any{
		"status_code":     statusCode,
		"body_bytes":      bodyBytes,
		"restored":        restored,
		"unknown":         unknown,
		"unknown_samples": unknownSamples,
		"skipped":         skipped,
	})
}

// streamSummary 是旧签名包装，保留给既有 trace 测试与调用方。
func (t traceLogger) streamSummary(chunks, restored, unknown, carryBytes, maxCarry int) {
	t.streamSummaryWith("", redact.StreamStats{
		Chunks: chunks, Restored: restored, Unknown: unknown, MaxPending: maxCarry,
	}, carryBytes)
}

// streamSummaryWith 记录/原地刷新 stream.summary（每块，TD-6）。
func (t traceLogger) streamSummaryWith(sourceFormat string, st redact.StreamStats, carryBytes int) {
	if !t.on() {
		return
	}
	t.recordLatest("stream.summary", map[string]any{
		"chunks": st.Chunks, "restored": st.Restored, "unknown": st.Unknown,
		"carry_bytes": carryBytes, "max_carry": st.MaxPending,
		"source_format":     sourceFormat,
		"unsupported_event": st.UnsupportedEvent, "missing_template": st.MissingTemplate,
		"leftover_at_end": st.LeftoverAtEnd, "dangling_samples": st.DanglingSamples,
	})
}

// streamFlush 是旧签名包装，保留给既有 trace 测试与调用方。
func (t traceLogger) streamFlush(flushed, flushedBytes, carryBytes int) {
	t.streamFlushWith("", redact.StreamStats{Flushed: flushed}, flushedBytes, carryBytes)
}

// streamFlushWith 记录 stream.flush（结束冲刷之后，需求 8）。
func (t traceLogger) streamFlushWith(sourceFormat string, st redact.StreamStats, flushedBytes, carryBytes int) {
	if !t.on() {
		return
	}
	t.record("stream.flush", map[string]any{
		"flushed": st.Flushed, "flushed_bytes": flushedBytes, "carry_bytes": carryBytes,
		"source_format":     sourceFormat,
		"unsupported_event": st.UnsupportedEvent, "missing_template": st.MissingTemplate,
		"leftover_at_end": st.LeftoverAtEnd, "dangling_samples": st.DanglingSamples,
	})
}

// pluginError 记录 plugin.error；不记 panic 原文（需求 5）。
func (t traceLogger) pluginError(reason, method, action string) {
	if !t.on() {
		return
	}
	t.record("plugin.error", map[string]any{"reason": reason, "method": method, "action": action})
}

// complete 记录 request.complete；duration_ms 缺 StartedAt 时由缓冲以首事件时间兜底。
func (t traceLogger) complete(outcome string, statusCode int, hasError bool, started, completed time.Time) {
	if !t.on() {
		return
	}
	fields := map[string]any{
		"outcome":     outcome,
		"status_code": statusCode,
		"has_error":   hasError,
	}
	if !started.IsZero() && !completed.IsZero() {
		fields["duration_ms"] = int(completed.Sub(started).Milliseconds())
	}
	t.record("request.complete", fields)
}

// findingAgg 聚合一次脱敏的 Finding：总数、按 Kind 计数、掩码明细（上限 64）。
type findingAgg struct {
	total        int
	counts       map[string]int
	details      []map[string]any
	truncated    int
	stale        int
	staleSamples []string
}

// newFindingAgg 构造聚合并返回 sink（挂到 WalkOptions.OnFinding）。
func newFindingAgg() *findingAgg {
	return &findingAgg{counts: map[string]int{}}
}

const staleSampleMax = 5

// staleSink 聚合过期占位符（nonce 不属于本请求）；样例已掩码，不含原值。
func (a *findingAgg) staleSink(_ []string, sample string) {
	a.stale++
	if len(a.staleSamples) < staleSampleMax {
		a.staleSamples = append(a.staleSamples, sample)
	}
}

func (a *findingAgg) sink(f redact.Finding) {
	a.total++
	a.counts[string(f.Kind)]++
	if len(a.details) < maxFindingDetails {
		a.details = append(a.details, map[string]any{
			"kind":        string(f.Kind),
			"placeholder": f.Placeholder,
			"preview":     f.Preview,
			"path":        f.Path,
			"reused":      f.Reused,
		})
	} else {
		a.truncated++
	}
}

// recordABIFailure 记录 abi.go 的 plugin.error；只解码 RequestID/TraceID 两个字段（设计 §3）。
func recordABIFailure(traces *traceBuffer, method, requestPayload, reason string) {
	if traces == nil || !traces.traceOn() {
		return
	}
	var payload struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
	}
	_ = json.Unmarshal([]byte(requestPayload), &payload)
	t := traceLogger{buf: traces, requestID: payload.RequestID, traceID: payload.TraceID}
	t.pluginError(reason, method, "none")
}
