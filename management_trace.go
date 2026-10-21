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
	// traceDataPath 是数据路由的完整管理路径（R12.1）：只有这个完整路径能取到数据。
	traceDataPath      = "/v0/management/privacygateway/traces"
	defaultTraceLimit  = 50
	defaultTraceMaxReq = 200 // 与配置默认 trace_max_requests 一致（未注册时的 limit 上限）
)

// managementTraceRoutes 返回 trace 数据路由与资源路由（需求 10.1–10.4）。
// 数据路由 GET / DELETE 完整管理路径（鉴权），
// 资源路由 {Path:"trace"}（不经鉴权，服务内嵌页面）。
func managementTraceRoutes() abiManagementRegistrationResponse {
	return abiManagementRegistrationResponse{
		Routes: []abiManagementRoute{
			{Method: "GET", Path: traceDataPath, Description: "读取内存中的请求脱敏追踪快照"},
			{Method: "DELETE", Path: traceDataPath, Description: "清空内存中的追踪缓冲"},
		},
		Resources: []abiResourceRoute{{
			Path:        traceResourcePath,
			Menu:        "隐私脱敏日志",
			Description: "查看每个请求的脱敏/还原事件流（仅内存，不含原值）",
		}},
	}
}

// handleABIManagementTrace 处理 management.handle 的 trace 数据/页面路由。
// 数据只在完整路径等于 /v0/management/privacygateway/traces 时返回（R12.1）；
// 资源通道 /v0/resource/... 与相对路径永远拿不到数据（R12.8）。
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

	switch {
	case dataRoutePath(req.Path) == strings.Trim(traceDataPath, "/"):
		return handleTraceData(ctx, req)
	case normalizeManagementPath(req.Path) == traceResourcePath:
		return handleTracePage(req)
	default:
		return abiOKEnvelope(managementPlainTextResponse(http.StatusNotFound, "privacy gateway management resource not found"))
	}
}

// dataRoutePath 归一化为数据路由判定路径：去 query、去首尾斜杠。
// 不折叠 /v0/resource/ 前缀，保证资源通道永远不匹配数据路由。
func dataRoutePath(path string) string {
	trimmed, _, _ := strings.Cut(path, "?")
	return strings.Trim(trimmed, "/")
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
			"Referrer-Policy":         []string{"no-referrer"},
			"Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'"},
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
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return abiOKEnvelope(traceJSONResponse(traceSnapshot{Enabled: false, Requests: []*traceRecord{}}))
	}
	maxRequests, _ := p.traces.stats()
	since, limit, filter := parseTraceQuery(req.Query, maxRequests)
	snap := p.traces.snapshot(since, limit, filter)
	if !snap.Enabled {
		snap.Requests = []*traceRecord{}
	}
	return abiOKEnvelope(traceJSONResponse(snap))
}

// handleTraceDataDelete 清空缓冲并返回 {"cleared":n}（R11.10）。
func handleTraceDataDelete(ctx context.Context) ([]byte, error) {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return abiOKEnvelope(traceClearResponse(0))
	}
	cleared := p.traces.clear()
	logHost(ctx, logLevelInfo, "privacy gateway trace buffer cleared", map[string]any{"cleared": cleared})
	return abiOKEnvelope(traceClearResponse(cleared))
}

func traceClearResponse(cleared int) pluginapi.ManagementResponse {
	body, _ := json.Marshal(map[string]int{"cleared": cleared})
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":           []string{"application/json; charset=utf-8"},
			"Cache-Control":          []string{"no-store"},
			"X-Content-Type-Options": []string{"nosniff"},
		},
		Body: body,
	}
}

// parseTraceQuery 解析 since / limit / filter 查询参数（R11.3–R11.7）。
// limit 默认 50，有效范围 1..trace_max_requests（maxRequests）；
// since/filter 无法解析或超范围时按 0/"" 处理，不返回 400。
func parseTraceQuery(query url.Values, maxRequests int) (since int64, limit int, filter string) {
	limit = defaultTraceLimit
	if maxRequests < 1 {
		maxRequests = defaultTraceMaxReq
	}
	if v := query.Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			since = n
		}
	}
	if v := query.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxRequests {
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
			"enabled":       snapPluginEnabled(),
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
		Headers: http.Header{
			"Content-Type":           []string{"application/json; charset=utf-8"},
			"Cache-Control":          []string{"no-store"},
			"X-Content-Type-Options": []string{"nosniff"},
		},
		Body: body,
	}
}

// snapPluginEnabled 返回当前插件总开关（未注册时 false）。
func snapPluginEnabled() bool {
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		return false
	}
	return p.enabled()
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

// normalizeManagementPath 归一化 management 页面路径：先去 query，
// 再去首尾斜杠，并把资源路由 /v0/resource/plugins/privacygateway/<name> 折叠为 <name>。
func normalizeManagementPath(path string) string {
	trimmed, _, _ := strings.Cut(path, "?")
	trimmed = strings.Trim(trimmed, "/")
	if strings.HasPrefix(trimmed, "v0/resource/plugins/privacygateway/") {
		return strings.TrimPrefix(trimmed, "v0/resource/plugins/privacygateway/")
	}
	return trimmed
}
