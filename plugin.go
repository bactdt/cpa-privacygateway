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

const pluginVersion = "0.1.3"

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
}

func newPlugin(cfg config) *plugin {
	return &plugin{
		cfg:      cfg,
		detector: cfg.redactConfig(),
		states:   newStateTable(cfg.MaxConcurrency, cfg.MaxRequests, cfg.ttl()),
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
	if strings.TrimSpace(req.RequestID) == "" {
		return p.handleEmptyRequestID(ctx, cfg, detector, req)
	}
	_, ok, existed := p.states.acquire(req.RequestID)
	if !ok {
		return terminate(http.StatusTooManyRequests, "rate_limit_exceeded", "privacy gateway concurrency limit reached", http.Header{"Retry-After": {"1"}})
	}
	if existed {
		// 复用条目不释放：首请求可能仍在途，拒绝本次正文即可（TD-CPA-034）。
		if blocked := p.guardRequest(cfg, req); blocked != nil {
			return *blocked, nil
		}
		return p.redactRequestWithExistingState(cfg, detector, req)
	}
	if blocked := p.guardRequest(cfg, req); blocked != nil {
		p.states.release(req.RequestID)
		return *blocked, nil
	}
	format := strings.TrimSpace(req.SourceFormat)
	if format != "" {
		p.states.setFormat(req.RequestID, format)
	}
	if !supportedFormats[format] {
		if cfg.failClosed() && format != "" {
			p.states.release(req.RequestID)
			return terminate(http.StatusUnsupportedMediaType, "invalid_request_error", "request format is not supported by privacy gateway", nil)
		}
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	return p.redactInterceptRequest(cfg, detector, format, req, true)
}

func (p *plugin) handleEmptyRequestID(ctx context.Context, cfg config, detector redact.Config, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if cfg.failClosed() {
		return terminate(http.StatusBadRequest, "invalid_request_error", "request id is required by privacy gateway", nil)
	}
	format := strings.TrimSpace(req.SourceFormat)
	if format == "" || !supportedFormats[format] || len(req.Body) == 0 {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	mapping, err := redact.NewMapping(cfg.MaxMappings, cfg.MaxMappingBytes)
	if err != nil {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	opts := redact.WalkOptions{Format: format, RedactToolArguments: cfg.RedactToolArguments}
	redacted, count, err := redact.RedactJSON(format, req.Body, mapping, detector, opts)
	if err != nil || count == 0 {
		// 无状态路径无映射可还原，任何错误都按未处理放行；
		// ErrFindings 同理，因为占位符在响应侧无法写回。
		return pluginapi.RequestInterceptResponse{}, nil
	}
	logHost(ctx, "warn", "request id is empty; redaction is stateless and placeholders cannot be restored", map[string]any{"source_format": format})
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

func (p *plugin) redactRequestWithExistingState(cfg config, detector redact.Config, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	format := strings.TrimSpace(req.SourceFormat)
	state := p.states.get(req.RequestID)
	if state == nil {
		return p.InterceptRequestBeforeAuth(context.Background(), req)
	}
	stateFormat := state.getFormat()
	stateMapping := state.getMapping()
	if stateFormat != "" && format != "" && stateFormat != format {
		return terminate(http.StatusBadRequest, "invalid_request_error", "request id reused with inconsistent source format", nil)
	}
	if stateMapping == nil {
		if format == "" {
			format = stateFormat
		}
		// 复用条目不释放：首请求可能仍在途，失败只影响本次正文（TD-CPA-034）。
		return p.redactInterceptRequest(cfg, detector, format, req, false)
	}
	if len(req.Body) == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	walkFormat := stateFormat
	if walkFormat == "" {
		walkFormat = format
	}
	opts := redact.WalkOptions{Format: walkFormat, RedactToolArguments: cfg.RedactToolArguments}
	redacted, count, err := redact.RedactJSON(walkFormat, req.Body, stateMapping, detector, opts)
	if err != nil {
		// 已有状态条目属于在途请求，失败时不得释放（首请求可能仍在途，TD-CPA-034）。
		return p.redactFailure(cfg, err, req)
	}
	if count == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

// redactInterceptRequest 为单次请求正文创建映射并脱敏，命中时写回状态表。
// ownsEntry 表示调用方是否独占该状态条目：新准入（或 after-auth 无状态）为 true，
// 复用已有条目为 false——false 时失败不得释放条目，首请求可能仍在途（TD-CPA-034）。
func (p *plugin) redactInterceptRequest(cfg config, detector redact.Config, format string, req pluginapi.RequestInterceptRequest, ownsEntry bool) (pluginapi.RequestInterceptResponse, error) {
	if len(req.Body) == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	mapping, err := redact.NewMapping(cfg.MaxMappings, cfg.MaxMappingBytes)
	if err != nil {
		if ownsEntry {
			p.states.release(req.RequestID)
		}
		if cfg.failClosed() {
			return terminate(http.StatusInternalServerError, "server_error", "privacy gateway mapping unavailable", nil)
		}
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	opts := redact.WalkOptions{Format: format, RedactToolArguments: cfg.RedactToolArguments}
	redacted, count, err := redact.RedactJSON(format, req.Body, mapping, detector, opts)
	if err != nil {
		if ownsEntry {
			p.states.release(req.RequestID)
		}
		return p.redactFailure(cfg, err, req)
	}
	if count == 0 {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	p.states.setMapping(req.RequestID, mapping)
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: redacted}, nil
}

func (p *plugin) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if strings.TrimSpace(req.RequestID) == "" {
		cfg, detector := p.settings()
		return p.handleEmptyRequestID(ctx, cfg, detector, req)
	}
	cfg, detector := p.settings()
	state := p.states.get(req.RequestID)
	if state == nil {
		_, ok, existed := p.states.acquire(req.RequestID)
		if !ok {
			return terminate(http.StatusTooManyRequests, "rate_limit_exceeded", "privacy gateway concurrency limit reached", http.Header{"Retry-After": {"1"}})
		}
		if blocked := p.guardRequest(cfg, req); blocked != nil {
			p.states.release(req.RequestID)
			return *blocked, nil
		}
		if existed {
			return p.redactRequestWithExistingState(cfg, detector, req)
		}
		return p.redactAfterAuthWithoutState(cfg, detector, req)
	}
	if blocked := p.guardRequest(cfg, req); blocked != nil {
		return *blocked, nil
	}
	return p.redactRequestWithExistingState(cfg, detector, req)
}

// guardRequest 对每个请求正文统一执行关键词拦截与大小上限检查，
// 不依赖准入路径（before/after-auth、重复 ID 均须通过）。
// 返回 nil 表示通过；否则返回应直接回复给宿主的拦截响应。
// 调用方负责条目的释放（复用条目不释放，新准入条目需释放）。
func (p *plugin) guardRequest(cfg config, req pluginapi.RequestInterceptRequest) *pluginapi.RequestInterceptResponse {
	for _, keyword := range cfg.keywords() {
		if bytes.Contains(req.Body, []byte(keyword)) {
			if keyword == "__panic_hook__" {
				// TD-044 验收注入点：宿主熔断语义要求插件自身 panic 被导出层 recover。
				panic("privacy gateway keyword guard test panic")
			}
			blocked, _ := terminate(http.StatusForbidden, "permission_error", "request blocked by privacy gateway policy", nil)
			return &blocked
		}
	}
	if len(req.Body) > cfg.MaxBodyBytes {
		if cfg.failClosed() {
			blocked, _ := terminate(http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds privacy gateway limit", nil)
			return &blocked
		}
	}
	return nil
}

// redactAfterAuthWithoutState 处理 after-auth 阶段没有状态条目的请求：
// 创建映射并完整脱敏，命中时写回状态表，保证 after-auth 新增的敏感值不会原文外发。
func (p *plugin) redactAfterAuthWithoutState(cfg config, detector redact.Config, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	format := strings.TrimSpace(req.SourceFormat)
	if format == "" || !supportedFormats[format] || len(req.Body) == 0 {
		return p.InterceptRequestBeforeAuth(context.Background(), req)
	}
	return p.redactInterceptRequest(cfg, detector, format, req, true)
}

func (p *plugin) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	if !p.enabled() {
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	mapping := p.states.mapping(req.RequestID)
	if mapping == nil || len(req.Body) == 0 {
		return pluginapi.ResponseInterceptResponse{}, nil
	}
	restored, count, err := redact.RestoreJSON(req.Body, mapping)
	if err != nil || count == 0 {
		return pluginapi.ResponseInterceptResponse{}, nil
	}
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
	if !changed {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	return pluginapi.StreamChunkInterceptResponse{Body: body}, nil
}

func (p *plugin) HandleRequestComplete(ctx context.Context, completion pluginapi.RequestCompletion) error {
	if strings.TrimSpace(completion.RequestID) == "" {
		return nil
	}
	restorer := p.states.streamRestorer(completion.RequestID)
	if restorer != nil {
		_ = restorer.Flush()
	}
	p.states.release(completion.RequestID)
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
func (p *plugin) redactFailure(cfg config, err error, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if errors.Is(err, redact.ErrCapacity) {
		if cfg.failClosed() {
			return terminate(http.StatusInternalServerError, "mapping_capacity_exceeded", "privacy gateway mapping capacity exceeded", nil)
		}
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if errors.Is(err, redact.ErrFindings) {
		if cfg.failClosed() {
			return terminate(http.StatusInternalServerError, "max_findings_exceeded", "privacy gateway detector findings exceeded max_findings", nil)
		}
		return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
	}
	if cfg.failClosed() {
		return terminate(http.StatusBadRequest, "invalid_request_error", "request body could not be parsed by privacy gateway", nil)
	}
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}, nil
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
