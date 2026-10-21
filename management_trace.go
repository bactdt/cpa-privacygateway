package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	traceResourcePath = "trace"
	traceDataPath     = "traces"
	defaultTraceLimit = 50
	maxTraceLimit     = 2000
)

// managementTraceRoutes 返回 trace 数据路由与资源路由（需求 12.1–12.4）。
// 数据路由 GET / DELETE /v0/management/privacygateway/traces（鉴权），
// 资源路由 {Path:"trace"}（不经鉴权，服务内嵌页面）。
func managementTraceRoutes() abiManagementRegistrationResponse {
	base := "privacygateway/" + traceDataPath
	return abiManagementRegistrationResponse{
		Routes: []abiManagementRoute{
			{Method: "GET", Path: base, Description: "读取内存中的请求脱敏追踪快照"},
			{Method: "DELETE", Path: base, Description: "清空内存中的追踪缓冲"},
		},
		Resources: []abiResourceRoute{{
			Path:        traceResourcePath,
			Menu:        "隐私脱敏日志",
			Description: "查看每个请求的脱敏/还原事件流（仅内存，不含原值）",
		}},
	}
}

// handleABIManagementTrace 处理 management.handle 的 trace 数据/页面路由。
func handleABIManagementTrace(ctx context.Context, request []byte) ([]byte, error) {
	var req abiManagementHandleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusBadRequest, "privacy gateway could not decode management request"))
	}

	// 宿主可能把查询串留在 Path 里（Query 字段为空）：回退解析，避免参数被忽略。
	if len(req.Query) == 0 {
		if _, pathWithQuery, found := strings.Cut(req.Path, "?"); found {
			if parsed, errParse := url.ParseQuery(pathWithQuery); errParse == nil {
				req.Query = parsed
			}
		}
	}

	normalized := normalizeManagementPath(req.Path)
	switch {
	case normalized == traceDataPath:
		return handleTraceData(ctx, req)
	case normalized == traceResourcePath:
		return handleTracePage(req)
	default:
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
}

// managementTraceAllowResponse 返回带显式 Allow 头的文本响应（数据路由支持 GET+DELETE）。
func managementTraceAllowResponse(status int, allow, message string) pluginapi.ManagementResponse {
	resp := managementPlainTextResponse(status, message)
	resp.Headers.Set("Allow", allow)
	return resp
}

// handleTracePage 服务内嵌 Trace_Page（需求 12.2–12.3，替换原 panel 页面）。
func handleTracePage(req abiManagementHandleRequest) ([]byte, error) {
	if !managementVisible() {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
	if req.Method != http.MethodGet {
		return abiOKEnvelope(managementResponseWithAllowHeader(http.StatusMethodNotAllowed, "privacy gateway management trace page only supports GET"))
	}
	return abiOKEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Cache-Control":           []string{"no-store"},
			"Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src *"},
		},
		Body: traceHTML,
	})
}

// handleTraceData 处理 GET / DELETE /v0/management/privacygateway/traces（需求 13）。
func handleTraceData(ctx context.Context, req abiManagementHandleRequest) ([]byte, error) {
	if !managementVisible() {
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
	switch req.Method {
	case http.MethodGet:
		return handleTraceDataGet(req)
	case http.MethodDelete:
		return handleTraceDataDelete(ctx)
	default:
		return abiOKEnvelope(managementTraceAllowResponse(http.StatusMethodNotAllowed, "GET, DELETE", "privacy gateway management trace data supports GET and DELETE"))
	}
}

func handleTraceDataGet(req abiManagementHandleRequest) ([]byte, error) {
	since, limit, filter := parseTraceQuery(req.Query)
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return abiOKEnvelope(traceJSONResponse(traceSnapshot{Enabled: false, Requests: []*traceRecord{}}))
	}
	snap := p.traces.snapshot(since, limit, filter)
	if !snap.Enabled {
		snap.Requests = []*traceRecord{}
	}
	return abiOKEnvelope(traceJSONResponse(snap))
}

func handleTraceDataDelete(ctx context.Context) ([]byte, error) {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return abiOKEnvelope(traceJSONResponse(traceSnapshot{Enabled: false, Requests: []*traceRecord{}}))
	}
	cleared := p.traces.clear()
	logHost(ctx, logLevelInfo, "privacy gateway trace buffer cleared", map[string]any{"cleared": cleared})
	return abiOKEnvelope(traceJSONResponse(p.traces.snapshot(0, 0, "")))
}

// parseTraceQuery 解析 since / limit / filter 查询参数（需求 13.3–13.6）。
// limit 默认 50，范围 1..maxTraceLimit；since/filter 非法时按 0/"" 处理。
func parseTraceQuery(query url.Values) (since int64, limit int, filter string) {
	limit = defaultTraceLimit
	if v := query.Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			since = n
		}
	}
	if v := query.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxTraceLimit {
			limit = n
		}
	}
	switch query.Get("filter") {
	case "", "all":
		filter = ""
	case "findings", "errors":
		filter = query.Get("filter")
	}
	return since, limit, filter
}

func traceJSONResponse(snap traceSnapshot) pluginapi.ManagementResponse {
	payload := map[string]any{
		"version": pluginVersion,
		"plugin": map[string]any{
			"version":       pluginVersion,
			"enabled":       true,
			"fail_closed":   gatewayFailClosed(),
			"trace_enabled": snap.Enabled,
		},
		"limits": map[string]any{
			"max_requests":        snapBufferedLimit(),
			"max_events":          snapEventsLimit(),
			"max_finding_details": maxFindingDetails,
		},
		"buffered":   snap.Buffered,
		"evicted":    snap.Evicted,
		"next_since": snap.NextSince,
		"requests":   snap.Requests,
	}
	body, _ := json.Marshal(payload)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func snapBufferedLimit() int {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return 0
	}
	maxRequests, _ := p.traces.stats()
	return maxRequests
}

func snapEventsLimit() int {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return 0
	}
	_, maxEvents := p.traces.stats()
	return maxEvents
}

// normalizeManagementPath 归一化 management 路径：去掉首尾斜杠，
// 并把资源路由 /v0/resource/plugins/privacygateway/<name> 折叠为 <name>。
func normalizeManagementPath(path string) string {
	trimmed := strings.Trim(path, "/")
	if strings.HasPrefix(trimmed, "v0/resource/plugins/privacygateway/") {
		return strings.TrimPrefix(trimmed, "v0/resource/plugins/privacygateway/")
	}
	if i := strings.Index(trimmed, "?"); i >= 0 {
		trimmed = trimmed[:i]
	}
	return trimmed
}
