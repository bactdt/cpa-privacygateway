package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"duanjuapp/cpa/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const pluginVersion = "0.1.4"

var supportedFormats = map[string]bool{
	"openai":          true,
	"openai-response": true,
	"claude":          true,
	"gemini":          true,
	"codex":           true,
	"antigravity":     true,
	"interactions":    true,
}

type plugin struct {
	mu       sync.RWMutex
	cfg      config
	detector redact.Config
	states   *stateTable
	traces   *traceBuffer // RT-1 请求脱敏追踪环形缓冲；开关由 reconfigure 应用
}

func newPlugin(cfg config) *plugin {
	return &plugin{
		cfg:      cfg,
		detector: cfg.redactConfig(),
		states:   newStateTable(cfg.MaxConcurrency, cfg.MaxRequests, cfg.ttl()),
		traces:   newTraceBuffer(),
	}
}

// enabled 读取插件总开关；与 reconfigure 并发安全。
func (p *plugin) enabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Enabled
}

func (p *plugin) reconfigure(cfg config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	p.detector = cfg.redactConfig()
	p.states.setLimits(cfg.MaxConcurrency, cfg.MaxRequests, cfg.ttl())
}

func (p *plugin) settings() (config, redact.Config) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg, p.detector
}

func (p *plugin) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	cfg, detector := p.settings()
	tl := p.trace(req.RequestID, req.TraceID)
	tl.received("before_auth", 0, req.SourceFormat, req.ToFormat, req.Model, req.RequestedModel, req.Stream, len(req.Body))
	if strings.TrimSpace(req.RequestID) == "" {
		return p.handleEmptyRequestID(ctx, cfg, detector, req, tl)
	}
	_, ok, existed := p.states.acquire(req.RequestID)
	if !ok {
		tl.rejected("concurrency_limit", http.StatusTooManyRequests)
		return terminate(http.StatusTooManyRequests, "rate_limit_exceeded", "privacy gateway concurrency limit reached", http.Header{"Retry-After": {"1"}})
	}
	if existed {
		// 复用条目不释放：首请求可能仍在途，拒绝本次正文即可（TD-CPA-034）。
		if blocked := p.guardRequest(cfg, req, tl); blocked != nil {
			return *blocked, nil
		}
		return p.redactRequestWithState(cfg, detector, req, false, tl)
	}
	if blocked := p.guardRequest(cfg, req, tl); blocked != nil {
		p.states.releaseIfUnmapped(req.RequestID)
		return *blocked, nil
	}
	return p.redactRequestWithState(cfg, detector, req, true, tl)
}

func (p *plugin) handleEmptyRequestID(ctx context.Context, cfg config, detector redact.Config, req pluginapi.RequestInterceptRequest, tl traceLogger) (pluginapi.RequestInterceptResponse, error) {
	if cfg.failClosed() {
		tl.rejected("missing_request_id", http.StatusBadRequest)
		return terminate(http.StatusBadRequest, "invalid_request_error", "request id is required by privacy gateway", nil)
	}
	format := strings.TrimSpace(req.SourceFormat)
	if format == "" || !supportedFormats[format] || len(req.Body) == 0 {
		tl.passthrough("stateless_unsupported")
		return pluginapi.RequestInterceptResponse{}, nil
	}
	mapping, err := redact.NewMapping(cfg.MaxMappings, cfg.MaxMappingBytes)
	if err != nil {
		tl.passthrough("mapping_unavailable")
		return pluginapi.RequestInterceptResponse{}, nil
	}
	redacted, count, agg, elapsed, err := redactObservedJSON(format, req.Body, mapping, detector, cfg, tl)
	if err != nil || count == 0 {
		// 无状态路径无映射可还原，任何错误都按未处理放行；
		// ErrFindings 同理，因为占位符在响应侧无法写回。
		if err != nil {
			tl.passthrough("stateless_redact_failed")
		} else {
			tl.passthrough("no_findings")
		}
		return pluginapi.RequestInterceptResponse{}, nil
	}
	tl.redactDone(agg, mapping.Size(), elapsed, true)
	logHost(ctx, "warn", "request id is empty; redaction is stateless and placeholders cannot be restored", map[string]any{"source_format": format})
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

func (p *plugin) redactRequestWithState(cfg config, detector redact.Config, req pluginapi.RequestInterceptRequest, ownsEntry bool, tl traceLogger) (pluginapi.RequestInterceptResponse, error) {
	state := p.states.get(req.RequestID)
	if state == nil {
		return p.InterceptRequestBeforeAuth(context.Background(), req)
	}
	format, consistent := state.useFormat(strings.TrimSpace(req.SourceFormat))
	if !consistent {
		tl.rejected("format_inconsistent", http.StatusBadRequest)
		return terminate(http.StatusBadRequest, "invalid_request_error", "request id reused with inconsistent source format", nil)
	}
	if !supportedFormats[format] {
		if cfg.failClosed() && format != "" {
			if ownsEntry {
				p.states.releaseIfUnmapped(req.RequestID)
			}
			tl.rejected("format_unsupported", http.StatusUnsupportedMediaType)
			return terminate(http.StatusUnsupportedMediaType, "invalid_request_error", "request format is not supported by privacy gateway", nil)
		}
		tl.passthrough("format_unsupported")
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	stateMapping := state.getMapping()
	if stateMapping == nil {
		return p.redactInterceptRequest(cfg, detector, format, req, ownsEntry, tl)
	}
	if len(req.Body) == 0 {
		tl.passthrough("empty_body")
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	redacted, count, agg, elapsed, err := redactObservedJSON(format, req.Body, stateMapping, detector, cfg, tl)
	if err != nil {
		// 已有状态条目属于在途请求，失败时不得释放（首请求可能仍在途，TD-CPA-034）。
		return p.redactFailure(cfg, err, req, tl)
	}
	tl.redactDone(agg, stateMapping.Size(), elapsed, false)
	if count == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

// redactInterceptRequest 为单次请求正文创建映射并脱敏，命中时写回状态表。
// ownsEntry 表示调用方是否独占该状态条目：新准入（或 after-auth 无状态）为 true，
// 复用已有条目为 false——false 时失败不得释放条目，首请求可能仍在途（TD-CPA-034）。
func (p *plugin) redactInterceptRequest(cfg config, detector redact.Config, format string, req pluginapi.RequestInterceptRequest, ownsEntry bool, tl traceLogger) (pluginapi.RequestInterceptResponse, error) {
	if len(req.Body) == 0 {
		tl.passthrough("empty_body")
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	mapping, err := redact.NewMapping(cfg.MaxMappings, cfg.MaxMappingBytes)
	if err != nil {
		if ownsEntry {
			p.states.releaseIfUnmapped(req.RequestID)
		}
		if cfg.failClosed() {
			tl.redactFailed("mapping_unavailable", "terminate", http.StatusInternalServerError)
			return terminate(http.StatusInternalServerError, "server_error", "privacy gateway mapping unavailable", nil)
		}
		tl.passthrough("mapping_unavailable")
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	redacted, count, agg, elapsed, err := redactObservedJSON(format, req.Body, mapping, detector, cfg, tl)
	if err != nil {
		if ownsEntry {
			p.states.releaseIfUnmapped(req.RequestID)
		}
		return p.redactFailure(cfg, err, req, tl)
	}
	if count == 0 {
		tl.redactDone(agg, mapping.Size(), elapsed, false)
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	ownedMapping, installed := p.states.setMapping(req.RequestID, mapping)
	if ownedMapping == nil {
		if ownsEntry {
			p.states.releaseIfUnmapped(req.RequestID)
		}
		if cfg.failClosed() {
			tl.redactFailed("mapping_unavailable", "terminate", http.StatusInternalServerError)
			return terminate(http.StatusInternalServerError, "server_error", "privacy gateway mapping unavailable", nil)
		}
		tl.passthrough("mapping_unavailable")
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if installed || ownedMapping == mapping {
		tl.redactDone(agg, mapping.Size(), elapsed, false)
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
	}
	// Another callback won the first-mapping race. Re-run against the winner so
	// the response can always be restored from the mapping held in the table.
	// 只对最终落地的映射记录 redact.done，避免双记（trace 计数含 race 丢弃的那次）。
	redacted, count, agg, elapsed, err = redactObservedJSON(format, req.Body, ownedMapping, detector, cfg, tl)
	if err != nil {
		return p.redactFailure(cfg, err, req, tl)
	}
	tl.redactDone(agg, ownedMapping.Size(), elapsed, false)
	if count == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

func (p *plugin) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if strings.TrimSpace(req.RequestID) == "" {
		cfg, detector := p.settings()
		return p.handleEmptyRequestID(ctx, cfg, detector, req, p.trace(req.RequestID, req.TraceID))
	}
	cfg, detector := p.settings()
	tl := p.trace(req.RequestID, req.TraceID)
	state := p.states.get(req.RequestID)
	attempt := 1
	if state != nil {
		attempt = state.nextAfterAuthAttempt()
	}
	tl.received("after_auth", attempt, req.SourceFormat, req.ToFormat, req.Model, req.RequestedModel, req.Stream, len(req.Body))
	if state == nil {
		st, ok, existed := p.states.acquire(req.RequestID)
		if st != nil {
			st.seedAfterAuthAttempt()
		}
		if !ok {
			tl.rejected("concurrency_limit", http.StatusTooManyRequests)
			return terminate(http.StatusTooManyRequests, "rate_limit_exceeded", "privacy gateway concurrency limit reached", http.Header{"Retry-After": {"1"}})
		}
		if blocked := p.guardRequest(cfg, req, tl); blocked != nil {
			if !existed {
				p.states.releaseIfUnmapped(req.RequestID)
			}
			return *blocked, nil
		}
		if existed {
			return p.redactRequestWithState(cfg, detector, req, false, tl)
		}
		return p.redactRequestWithState(cfg, detector, req, true, tl)
	}
	if blocked := p.guardRequest(cfg, req, tl); blocked != nil {
		return *blocked, nil
	}
	return p.redactRequestWithState(cfg, detector, req, false, tl)
}

// guardRequest 对每个请求正文统一执行关键词拦截与大小上限检查，
// 不依赖准入路径（before/after-auth、重复 ID 均须通过）。
// 返回 nil 表示通过；否则返回应直接回复给宿主的拦截响应。
// 调用方负责条目的释放（复用条目不释放，新准入条目需释放）。
func (p *plugin) guardRequest(cfg config, req pluginapi.RequestInterceptRequest, tl traceLogger) *pluginapi.RequestInterceptResponse {
	for _, keyword := range cfg.keywords() {
		if bytes.Contains(req.Body, []byte(keyword)) {
			if keyword == "__panic_hook__" {
				// TD-044 验收注入点：宿主熔断语义要求插件自身 panic 被导出层 recover。
				panic("privacy gateway keyword guard test panic")
			}
			// 记录命中类型但绝不记录关键词本身（需求 5/TD-CPA-018）。
			tl.blocked("reject_keyword", http.StatusForbidden, len(req.Body), 0)
			blocked, _ := terminate(http.StatusForbidden, "permission_error", "request blocked by privacy gateway policy", nil)
			return &blocked
		}
	}
	if len(req.Body) > cfg.MaxBodyBytes {
		if cfg.failClosed() {
			tl.blocked("body_too_large", http.StatusRequestEntityTooLarge, len(req.Body), cfg.MaxBodyBytes)
			blocked, _ := terminate(http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds privacy gateway limit", nil)
			return &blocked
		}
		tl.passthrough("body_too_large")
	}
	return nil
}

func (p *plugin) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	tl := p.trace(req.RequestID, "")
	mapping := p.states.mapping(req.RequestID)
	if mapping == nil {
		tl.responseRestored(req.StatusCode, len(req.Body), 0, 0, nil, "no_mapping")
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	if len(req.Body) == 0 {
		tl.responseRestored(req.StatusCode, 0, 0, 0, nil, "empty_body")
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	restored, stats, err := redact.RestoreJSONCounted(req.Body, mapping)
	if err != nil || stats.Restored == 0 {
		tl.responseRestored(req.StatusCode, len(req.Body), stats.Restored, stats.Unknown, stats.UnknownSamples, "parse_failed")
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	tl.responseRestored(req.StatusCode, len(req.Body), stats.Restored, stats.Unknown, stats.UnknownSamples, "")
	return pluginapi.ResponseInterceptResponse{Body: restored}, nil
}

func (p *plugin) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	restorer := p.states.streamRestorer(req.RequestID)
	if restorer == nil || len(req.Body) == 0 {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	// 宿主不保证 chunk 形态（裸 JSON / 单行 SSE / 完整帧）；RestorePayload 按形态分派，
	// 跨块占位符以合成帧/暂扣方式处理，永不要求宿主丢弃 chunk。
	body, changed := restorer.RestorePayload(req.SourceFormat, req.Body)
	// stream.summary 每块原地更新（TD-6）；块内容本身绝不入库（需求 5）。
	if tl := p.trace(req.RequestID, ""); tl.on() {
		st := restorer.Stats()
		tl.streamSummary(st.Chunks, st.Restored, st.Unknown, restorer.Pending(), st.MaxPending)
	}
	if !changed {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	return pluginapi.StreamChunkInterceptResponse{Body: body}, nil
}

func (p *plugin) HandleRequestComplete(ctx context.Context, completion pluginapi.RequestCompletion) error {
	if strings.TrimSpace(completion.RequestID) == "" {
		return nil
	}
	tl := p.trace(completion.RequestID, completion.TraceID)
	restorer := p.states.streamRestorer(completion.RequestID)
	if restorer != nil {
		carry := restorer.Pending()
		out := restorer.Flush()
		st := restorer.Stats()
		// 仅在确有冲刷内容/流活动时记录 stream.flush，避免非流式请求产生噪音事件。
		if carry > 0 || len(out) > 0 || st.Flushed > 0 || st.Chunks > 0 {
			tl.streamFlush(st.Flushed, len(out), carry)
		}
	}
	p.states.release(completion.RequestID)
	tl.complete(string(completion.Outcome), completion.StatusCode, completion.Error != "", completion.StartedAt, completion.CompletedAt)
	return nil
}

func (p *plugin) runJanitor(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			p.states.janitor(now)
		}
	}
}

// redactFailure 统一 RedactJSON 失败的响应语义：
// 容量与检出超限是网关自身资源/策略限制，fail_closed 返回 500；
// 其余按正文解析失败处理，fail_closed 返回 400，fail_open 原样放行。
func (p *plugin) redactFailure(cfg config, err error, req pluginapi.RequestInterceptRequest, tl traceLogger) (pluginapi.RequestInterceptResponse, error) {
	if errors.Is(err, redact.ErrCapacity) {
		if cfg.failClosed() {
			tl.redactFailed("capacity", "terminate", http.StatusInternalServerError)
			return terminate(http.StatusInternalServerError, "mapping_capacity_exceeded", "privacy gateway mapping capacity exceeded", nil)
		}
		tl.redactFailed("capacity", "passthrough", 0)
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if errors.Is(err, redact.ErrFindings) {
		if cfg.failClosed() {
			tl.redactFailed("max_findings", "terminate", http.StatusInternalServerError)
			return terminate(http.StatusInternalServerError, "max_findings_exceeded", "privacy gateway detector findings exceeded max_findings", nil)
		}
		tl.redactFailed("max_findings", "passthrough", 0)
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if cfg.failClosed() {
		tl.redactFailed("malformed_json", "terminate", http.StatusBadRequest)
		return terminate(http.StatusBadRequest, "invalid_request_error", "request body could not be parsed by privacy gateway", nil)
	}
	tl.redactFailed("malformed_json", "passthrough", 0)
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
}

// redactObservedJSON 包装 redact.RedactJSON：追踪开启时挂载 findingAgg sink，
// 并返回耗时供 redact.done 使用。sink 关闭时输出与原调用逐字节一致（需求 4.4）。
func redactObservedJSON(format string, body []byte, mapping *redact.Mapping, detector redact.Config, cfg config, tl traceLogger) ([]byte, int, *findingAgg, time.Duration, error) {
	opts := redact.WalkOptions{Format: format, RedactToolArguments: cfg.RedactToolArguments}
	agg := newFindingAgg()
	if tl.on() {
		opts.OnFinding = agg.sink
	}
	start := time.Now()
	redacted, count, err := redact.RedactJSON(format, body, mapping, detector, opts)
	return redacted, count, agg, time.Since(start), err
}

func terminate(status int, errorType, message string, headers http.Header) (pluginapi.RequestInterceptResponse, error) {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
	if err != nil {
		return pluginapi.RequestInterceptResponse{}, err
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: headers,
		ResponseBody:    body,
	}, nil
}
