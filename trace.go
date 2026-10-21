package main

import (
	"fmt"
	"sync"
	"time"
)

// traceEvent 是单请求记录中的一条阶段事件。字段值只允许代码内常量、
// redact.Finding 与计数，绝不携带敏感原值（需求 5）。
type traceEvent struct {
	Seq    int64          `json:"seq"`
	At     time.Time      `json:"at"`
	Stage  string         `json:"stage"`
	Fields map[string]any `json:"fields,omitempty"`
}

// traceRecord 是以 RequestID 为键的单请求记录。
type traceRecord struct {
	RequestID         string         `json:"request_id"`
	TraceID           string         `json:"trace_id,omitempty"`
	Anonymous         bool           `json:"anonymous"`
	Partial           bool           `json:"partial"`
	FirstSeen         time.Time      `json:"first_seen"`
	UpdatedSeq        int64          `json:"updated_seq"`
	Outcome           string         `json:"outcome,omitempty"`
	Status            int            `json:"status,omitempty"`
	Terminated        bool           `json:"terminated"`
	HasError          bool           `json:"has_error"`
	FindingTotal      int            `json:"finding_total"`
	Counts            map[string]int `json:"counts,omitempty"`
	DroppedEvents     int            `json:"dropped_events,omitempty"`
	FindingsTruncated int            `json:"findings_truncated,omitempty"`
	Events            []traceEvent   `json:"events"`
}

// traceBuffer 是有界环形缓冲：最多 maxRequests 个记录、每记录最多 maxEvents 个事件。
// 所有方法并发安全；记录过程中的 panic 被恢复并丢弃该事件（需求 6.9）。
type traceBuffer struct {
	mu          sync.Mutex
	enabled     bool
	recording   bool
	maxRequests int
	maxEvents   int
	records     map[string]*traceRecord
	order       []string        // 首次出现顺序，用于淘汰最旧
	recentLost  map[string]bool // 最近被淘汰的 RequestID，用于 partial 判定
	lostOrder   []string        // recentLost 的 FIFO 顺序，有界
	seq         int64
	evicted     int64
}

const maxFindingDetails = 64 // 需求 6.6：每记录 Finding 明细上限

func newTraceBuffer() *traceBuffer {
	// 容器本身默认记录；是否记录由 plugin 按配置/页面开关注入（setRecording）。
	return &traceBuffer{recording: true, records: map[string]*traceRecord{}, recentLost: map[string]bool{}}
}

// configure 应用开关与容量；关闭时清空全部记录（需求 7.4）。
// 缩容时按首次出现顺序淘汰最旧记录直至达标（需求 7.3）。
func (b *traceBuffer) configure(enabled bool, maxRequests, maxEvents int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled = enabled
	b.maxRequests, b.maxEvents = maxRequests, maxEvents
	if !enabled {
		b.recording = false
		b.records = map[string]*traceRecord{}
		b.order = nil
		b.recentLost = map[string]bool{}
		b.lostOrder = nil
		return
	}
	for len(b.order) > b.maxRequests {
		b.evictOldestLocked()
	}
}

// traceOn 报告是否正在记录（拦截器快速路径，需求 7.5/7.6）：
// 配置开关打开且页面已点「开始记录」才为真。
func (b *traceBuffer) traceOn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled && b.recording
}

// recordingOn 报告记录开关状态（页面按钮与 GET 响应用）。
func (b *traceBuffer) recordingOn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled && b.recording
}

// setRecording 开关记录：由关到开时清空旧记录，返回(turnedOn, cleared)，
// 保证一次记录会话只包含点击「开始记录」之后发生的请求（需求 7.6）。
// 关闭时保留已有记录，便于继续阅读。追踪总开关关闭时无法开始记录。
func (b *traceBuffer) setRecording(on bool) (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		b.recording = false
		return false, 0
	}
	if b.recording == on {
		return on, 0
	}
	b.recording = on
	if !on {
		return false, 0
	}
	cleared := len(b.records)
	b.records = map[string]*traceRecord{}
	b.order = nil
	b.recentLost = map[string]bool{}
	b.lostOrder = nil
	b.evicted = 0
	return true, cleared
}

// record 追加一条事件；RequestID 为空时以 anon-<seq> 合成键（需求 6.8）。
func (b *traceBuffer) record(requestID, traceID, stage string, fields map[string]any) {
	b.recordMode(requestID, traceID, stage, fields, false)
}

// recordLatest 与 record 相同，但当末尾事件同为 stage 时原地更新其字段
// （stream.summary 每块原地刷新，TD-6/需求 6.4 不受事件数上限影响）。
func (b *traceBuffer) recordLatest(requestID, traceID, stage string, fields map[string]any) {
	b.recordMode(requestID, traceID, stage, fields, true)
}

func (b *traceBuffer) recordMode(requestID, traceID, stage string, fields map[string]any, replaceLast bool) {
	defer func() { _ = recover() }() // 需求 6.9
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled || !b.recording {
		return
	}
	b.seq++
	rec, created := b.lookupOrCreateLocked(requestID)
	if created && stage != "request.received" {
		// 记录中途开启：该请求更早的阶段没有被采集。
		rec.Partial = true
	}
	if replaceLast && len(rec.Events) > 0 && rec.Events[len(rec.Events)-1].Stage == stage {
		last := &rec.Events[len(rec.Events)-1]
		last.Seq = b.seq
		last.At = time.Now()
		last.Fields = fields
		rec.UpdatedSeq = b.seq
		if traceID != "" {
			rec.TraceID = traceID
		}
		return
	}
	b.appendEventLocked(rec, traceID, stage, fields)
}

// evictOldestLocked 淘汰最早记录，并把其 ID 记入 recentLost 供 partial 判定。
func (b *traceBuffer) evictOldestLocked() {
	if len(b.order) == 0 {
		return
	}
	key := b.order[0]
	b.order = b.order[1:]
	delete(b.records, key)
	b.evicted++
	if !b.recentLost[key] {
		b.recentLost[key] = true
		b.lostOrder = append(b.lostOrder, key)
		for len(b.lostOrder) > b.maxRequests {
			delete(b.recentLost, b.lostOrder[0])
			b.lostOrder = b.lostOrder[1:]
		}
	}
}

// lookupOrCreateLocked 查找或新建记录；满员时淘汰最旧（需求 6.2），
// 被本缓冲淘汰后再次出现的请求则新建并标记 partial（需求 6.3）。
func (b *traceBuffer) lookupOrCreateLocked(requestID string) (*traceRecord, bool) {
	key := requestID
	if key == "" {
		key = fmt.Sprintf("anon-%d", b.seq)
	}
	if rec, ok := b.records[key]; ok {
		return rec, false
	}
	partial := requestID != "" && b.recentLost[key]
	if partial {
		delete(b.recentLost, key)
	}
	for len(b.order) >= b.maxRequests && len(b.order) > 0 {
		b.evictOldestLocked()
	}
	rec := &traceRecord{
		RequestID: key,
		Anonymous: requestID == "",
		Partial:   partial,
		FirstSeen: time.Now(),
	}
	b.records[key] = rec
	b.order = append(b.order, key)
	return rec, true
}

// appendEventLocked 追加事件并更新记录汇总字段。
// 事件数达 maxEvents-1 时丢弃除 request.complete 以外的新事件（需求 6.4）。
func (b *traceBuffer) appendEventLocked(rec *traceRecord, traceID, stage string, fields map[string]any) {
	if len(rec.Events) >= b.maxEvents-1 && stage != "request.complete" {
		rec.DroppedEvents++
		return
	}
	ev := traceEvent{Seq: b.seq, At: time.Now(), Stage: stage, Fields: fields}
	if len(rec.Events) > 0 && ev.At.Before(rec.Events[len(rec.Events)-1].At) {
		// at 单调不减（需求 6.7）：乱序时钳到上一事件时间
		ev.At = rec.Events[len(rec.Events)-1].At
	}
	rec.Events = append(rec.Events, ev)
	rec.UpdatedSeq = b.seq
	if traceID != "" {
		rec.TraceID = traceID // 需求 18.1/18.3：后续空 TraceID 不覆盖已有值
	}
	if stage == "request.complete" {
		if _, ok := fields["duration_ms"]; !ok {
			// 缺 StartedAt 时用首事件时间兜底（需求 18 事件表）
			fields["duration_ms"] = int(ev.At.Sub(rec.FirstSeen).Milliseconds())
		}
	}
	b.applySummaryLocked(rec, stage, fields)
}

// applySummaryLocked 按阶段更新记录级汇总字段。
func (b *traceBuffer) applySummaryLocked(rec *traceRecord, stage string, fields map[string]any) {
	switch stage {
	case "redact.done":
		if n, ok := fields["findings"].(int); ok {
			rec.FindingTotal += n
		}
		if n, ok := fields["findings_truncated"].(int); ok {
			rec.FindingsTruncated += n
		}
		if counts, ok := fields["counts"].(map[string]int); ok {
			if rec.Counts == nil {
				rec.Counts = map[string]int{}
			}
			for k, v := range counts {
				rec.Counts[k] += v
			}
		}
	case "request.rejected", "guard.blocked":
		rec.Terminated = true
		if s, ok := fields["status"].(int); ok {
			rec.Status = s
		}
	case "redact.failed":
		if action, ok := fields["action"].(string); ok && action == "terminate" {
			rec.Terminated = true
		}
	case "plugin.error":
		rec.HasError = true
	case "request.complete":
		if o, ok := fields["outcome"].(string); ok {
			rec.Outcome = o
		}
		if s, ok := fields["status_code"].(int); ok {
			rec.Status = s
		}
		if he, ok := fields["has_error"].(bool); ok {
			rec.HasError = rec.HasError || he
		}
	}
}

// clear 清空全部记录并返回清空前的记录数（需求 11.10）。
func (b *traceBuffer) clear() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.records)
	b.records = map[string]*traceRecord{}
	b.order = nil
	return n
}

// traceSnapshot 是 GET 响应的数据部分。
type traceSnapshot struct {
	Enabled   bool           `json:"trace_enabled"`
	Recording bool           `json:"recording"`
	Buffered  int            `json:"buffered"`
	Evicted   int64          `json:"evicted"`
	NextSince int64          `json:"next_since"`
	Requests  []*traceRecord `json:"requests"`
}

// snapshot 返回按 first_seen 倒序、最多 limit 条、updated_seq>since、
// 满足 filter 的记录；NextSince 不小于任何返回记录的 updated_seq（需求 11）。
func (b *traceBuffer) snapshot(since int64, limit int, filter string) traceSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snap := traceSnapshot{Enabled: b.enabled, Recording: b.enabled && b.recording, NextSince: b.seq, Evicted: b.evicted}
	if !b.enabled {
		snap.Requests = []*traceRecord{}
		return snap
	}
	matched := make([]*traceRecord, 0, len(b.records))
	for _, key := range b.order {
		rec := b.records[key]
		if rec.UpdatedSeq <= since {
			continue
		}
		if !matchTraceFilter(rec, filter) {
			continue
		}
		matched = append(matched, rec)
	}
	// first_seen 倒序
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	snap.Buffered = len(b.records)
	snap.Requests = matched
	return snap
}

// matchTraceFilter：findings=有检出；errors=终止/错误/失败结局（需求 11.5/11.6）。
func matchTraceFilter(rec *traceRecord, filter string) bool {
	switch filter {
	case "findings":
		return rec.FindingTotal > 0
	case "errors":
		return rec.Terminated || rec.HasError ||
			rec.Outcome == "failed" || rec.Outcome == "rejected" || rec.Outcome == "canceled"
	default:
		return true
	}
}

// stats 返回容量配置，供 GET 响应的 limits 字段。
func (b *traceBuffer) stats() (maxRequests, maxEvents int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxRequests, b.maxEvents
}
