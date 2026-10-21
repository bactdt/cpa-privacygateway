package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func enablePanelForTrace(t *testing.T) {
	t.Helper()
	resetABIState(t)
	abiState.Lock()
	abiState.panelEnabled = true
	abiState.traceEnabled = true
	abiState.Unlock()
	t.Cleanup(func() { resetABIState(t) })
}

// traceJSONBody 解码 traces 数据路由返回的 payload（callManagement 已解外层信封）。
func traceJSONBody(t *testing.T, resp pluginapi.ManagementResponse) map[string]json.RawMessage {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d，期望 200，body=%s", resp.StatusCode, resp.Body)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q，期望 application/json; charset=utf-8", ct)
	}
	var payload map[string]json.RawMessage
	if errDecode := json.Unmarshal(resp.Body, &payload); errDecode != nil {
		t.Fatalf("body JSON 解码失败：%v (%s)", errDecode, resp.Body)
	}
	return payload
}

func TestTraceDataPageRequiresPanelEnabled(t *testing.T) {
	resetABIState(t)
	for _, path := range []string{"trace", traceDataPath} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			if got := callManagement(t, method, path).StatusCode; got != http.StatusNotFound {
				t.Errorf("panel.enabled=false 时 %s %s status = %d，期望 404", method, path, got)
			}
		}
	}
}

func TestTraceDataGetShapeAndQuery(t *testing.T) {
	enablePanelForTrace(t)
	registerTrace(t, "panel:\n  enabled: true\ntrace_enabled: true\ntrace_max_requests: 5\ntrace_max_events: 8\n")
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p == nil {
		t.Fatal("register 后 plugin 应非空")
	}
	p.traces.record("r1", "t1", "request.received", nil)
	p.traces.record("r2", "t2", "redact.done", map[string]any{
		"findings": 3,
		"counts":   map[string]int{"email": 2, "phone": 1},
	})

	// 默认查询：since=0, limit=50, filter=""
	payload := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	for _, key := range []string{"version", "plugin", "limits", "recording", "buffered", "evicted", "next_since", "requests"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("traces 响应缺少字段 %q：%s", key, payload)
		}
	}
	var requests []map[string]any
	if errDecode := json.Unmarshal(payload["requests"], &requests); errDecode != nil {
		t.Fatalf("requests 解码失败：%v", errDecode)
	}
	if len(requests) != 2 {
		t.Errorf("requests 数量 = %d，期望 2", len(requests))
	}
	// 汇总字段：redact.done 的 findings/counts 应写入记录级字段。
	byID := map[string]map[string]any{}
	for _, r := range requests {
		byID[r["request_id"].(string)] = r
	}
	if r2 := byID["r2"]; r2 == nil {
		t.Fatal("缺少 r2 记录")
	} else {
		if got := r2["finding_total"].(float64); got != 3 {
			t.Errorf("r2 finding_total = %v，期望 3", got)
		}
		counts := map[string]float64{}
		if raw, errMarshal := json.Marshal(r2["counts"]); errMarshal == nil {
			_ = json.Unmarshal(raw, &counts)
		}
		if counts["email"] != 2 || counts["phone"] != 1 {
			t.Errorf("r2 counts = %v，期望 email=2 phone=1", counts)
		}
	}
	var limits struct {
		MaxRequests       int `json:"max_requests"`
		MaxEvents         int `json:"max_events"`
		MaxFindingDetails int `json:"max_finding_details"`
	}
	if errDecode := json.Unmarshal(payload["limits"], &limits); errDecode != nil {
		t.Fatalf("limits 解码失败：%v", errDecode)
	}
	if limits.MaxRequests != 5 || limits.MaxEvents != 8 || limits.MaxFindingDetails != maxFindingDetails {
		t.Errorf("limits = %+v，期望 5/8/%d", limits, maxFindingDetails)
	}

	// since 过滤：只返回 updated_seq 更大的记录。
	sinceSeq := requests[0]["updated_seq"].(float64)
	sinceQuery := traceDataPath + "?since=" + itoa64(int64(sinceSeq))
	filtered := traceJSONBody(t, callManagement(t, http.MethodGet, sinceQuery))
	var after []map[string]any
	_ = json.Unmarshal(filtered["requests"], &after)
	if len(after) != 0 {
		t.Errorf("since=最新序号后应返回 0 条，实际 %d", len(after))
	}

	// limit=1 截断。
	limited := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath+"?limit=1"))
	var one []map[string]any
	_ = json.Unmarshal(limited["requests"], &one)
	if len(one) != 1 {
		t.Errorf("limit=1 应返回 1 条，实际 %d", len(one))
	}

	// 非法 limit 回退默认 50（此处 2 条全部返回）。
	invalid := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath+"?limit=abc&limit=99999"))
	var invalidReqs []map[string]any
	_ = json.Unmarshal(invalid["requests"], &invalidReqs)
	if len(invalidReqs) != 2 {
		t.Errorf("非法 limit 应回退默认，返回 %d 条，期望 2", len(invalidReqs))
	}

	// filter=findings：只保留带 finding 的记录。
	findings := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath+"?filter=findings"))
	var fReqs []map[string]any
	_ = json.Unmarshal(findings["requests"], &fReqs)
	if len(fReqs) != 1 || fReqs[0]["request_id"] != "r2" {
		t.Errorf("filter=findings 应只返回 r2，实际 %s", findings["requests"])
	}
	// filter 非法值回退 all。
	all := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath+"?filter=bogus"))
	var aReqs []map[string]any
	_ = json.Unmarshal(all["requests"], &aReqs)
	if len(aReqs) != 2 {
		t.Errorf("非法 filter 应回退 all，返回 %d 条，期望 2", len(aReqs))
	}
}

func TestTraceDataDeleteClearsBuffer(t *testing.T) {
	enablePanelForTrace(t)
	registerTrace(t, "panel:\n  enabled: true\ntrace_enabled: true\ntrace_max_requests: 5\n")
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	p.traces.record("r1", "t1", "request.received", nil)

	deleted := traceJSONBody(t, callManagement(t, http.MethodDelete, traceDataPath))
	var cleared int
	if errDecode := json.Unmarshal(deleted["cleared"], &cleared); errDecode != nil {
		t.Fatalf("cleared 解码失败：%v (%s)", errDecode, deleted["cleared"])
	}
	if cleared != 1 {
		t.Errorf("DELETE cleared = %d，期望 1（清空前的记录数）", cleared)
	}
	if got := len(p.traces.snapshot(0, 10, "").Requests); got != 0 {
		t.Errorf("DELETE 后仍剩 %d 条记录", got)
	}
	// DELETE 后 GET 返回空数组。
	empty := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	var reqs []map[string]any
	_ = json.Unmarshal(empty["requests"], &reqs)
	if len(reqs) != 0 {
		t.Errorf("清空后 GET 返回 %d 条，期望 0", len(reqs))
	}
}

func TestTraceDataRejectsUnsupportedMethod(t *testing.T) {
	enablePanelForTrace(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		resp := callManagement(t, method, traceDataPath)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s traces status = %d，期望 405", method, resp.StatusCode)
		}
		if got := resp.Headers.Get("Allow"); !strings.Contains(got, "GET") || !strings.Contains(got, "DELETE") {
			t.Errorf("Allow = %q，期望含 GET/DELETE", got)
		}
	}
}

// TestTraceDataOnlyViaFullManagementPath 覆盖 R12.1/R12.8 数据隔离：
// 资源通道与相对路径永远拿不到追踪数据，只有完整管理路径可以。
func TestTraceDataOnlyViaFullManagementPath(t *testing.T) {
	enablePanelForTrace(t)
	registerTrace(t, "panel:\n  enabled: true\ntrace_enabled: true\n")
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p != nil {
		p.traces.record("r1", "t1", "redact.done", map[string]any{"findings": 1})
	}

	for _, path := range []string{
		"traces",
		"/traces",
		"/v0/resource/plugins/privacygateway/traces",
		"/v0/resource/plugins/privacygateway/traces?limit=1",
		"/v0/management/plugins/privacygateway/traces",
		"/v0/management/privacygateway/panel",
		"panel",
	} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			if got := callManagement(t, method, path).StatusCode; got != http.StatusNotFound {
				t.Errorf("%s %s status = %d，期望 404（资源通道/非管理路径不得取到数据）", method, path, got)
			}
		}
	}

	// 完整管理路径 + 查询串可取到数据。
	ok := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath+"?since=0&limit=10"))
	if len(ok["requests"]) == 0 {
		t.Error("完整管理路径应能取到追踪数据")
	}

	// 旧 panel 路径不再服务（404）。
	if got := callManagement(t, http.MethodGet, "/v0/resource/plugins/privacygateway/panel").StatusCode; got != http.StatusNotFound {
		t.Errorf("旧 panel 路径 status = %d，期望 404", got)
	}
}

func TestTraceDataWithoutPluginReturnsEmptySnapshot(t *testing.T) {
	enablePanelForTrace(t)
	payload := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	var plug struct {
		TraceEnabled bool `json:"trace_enabled"`
	}
	if errDecode := json.Unmarshal(payload["plugin"], &plug); errDecode != nil {
		t.Fatalf("plugin 解码失败：%v", errDecode)
	}
	if plug.TraceEnabled {
		t.Error("未注册时 trace_enabled 应为 false")
	}
	var reqs []map[string]any
	if errDecode := json.Unmarshal(payload["requests"], &reqs); errDecode != nil {
		t.Fatalf("requests 解码失败：%v", errDecode)
	}
	if len(reqs) != 0 {
		t.Errorf("未注册时 requests 应为空，实际 %d 条", len(reqs))
	}
}

func TestTracePageServesEmbeddedHTML(t *testing.T) {
	enablePanelForTrace(t)
	resp := callManagement(t, http.MethodGet, "trace")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET trace status = %d，期望 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := resp.Headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q，期望 nosniff", got)
	}
	if got := resp.Headers.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q，期望 no-store", got)
	}
	html := string(resp.Body)
	if !strings.Contains(html, "location.origin") {
		t.Error("trace 页应包含 location.origin 默认地址逻辑")
	}
	if strings.Contains(html, "<script src") || strings.Contains(html, "<link ") {
		t.Error("trace 页不应引用外部资源")
	}
	// 资源路径折叠 /v0/resource/plugins/privacygateway/trace。
	folded := callManagement(t, http.MethodGet, "/v0/resource/plugins/privacygateway/trace")
	if folded.StatusCode != http.StatusOK {
		t.Errorf("资源路径折叠 GET status = %d，期望 200", folded.StatusCode)
	}
}

// TestTracePageSourceInvariants 覆盖 R14–R16 与 R15 的页面源码不变式。
func TestTracePageSourceInvariants(t *testing.T) {
	enablePanelForTrace(t)
	html := string(callManagement(t, http.MethodGet, "trace").Body)

	for _, forbidden := range []string{"innerHTML", "localStorage", "<script src", "http://", "https://"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("trace 页源码不应包含 %q", forbidden)
		}
	}
	for _, want := range []string{
		"AbortController",
		"8000",
		"Authorization",
		"sessionStorage",
		"Bearer ",
		`role="status"`,
		`aria-live="polite"`,
		`role="alert"`,
		`createElement("details")`,
		"textContent",
		"/v0/management/privacygateway/traces",
		"请输入 CPA 管理密钥",
		"暂无请求。请通过 CPA 发送一次模型请求，再回到此页",
		"追踪已关闭",
		"插件总开关关闭，请求不会被脱敏，也不会被记录",
		"请求超时（8s）",
		"插件版本过旧或未注册管理路由",
		"密钥错误，已停止自动刷新以免触发宿主封禁",
		"trace [",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("trace 页源码应包含 %q", want)
		}
	}
}

// TestManagementVisibleRequiresBothSwitches 覆盖 TD-6 联动语义：
// panel.enabled=true 但 trace_enabled=false 时不声明管理能力、路由与页面均 404。
func TestManagementVisibleRequiresBothSwitches(t *testing.T) {
	resetABIState(t)
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister,
		lifecycleRequest(t, []byte("panel:\n  enabled: true\ntrace_enabled: false\n"))); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { resetABIState(t) })

	raw, err := handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Capabilities map[string]bool `json:"capabilities"`
			Resources    []struct {
				Path string `json:"Path"`
			} `json:"resources"`
			Routes []json.RawMessage `json:"routes"`
		} `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("信封异常：%s", raw)
	}
	if env.Result.Capabilities["management_api"] {
		t.Errorf("trace_enabled=false 时 management_api 不应声明为 true：%s", raw)
	}
	if len(env.Result.Resources) != 0 || len(env.Result.Routes) != 0 {
		t.Errorf("trace_enabled=false 时 resources/routes 应为空：%s", raw)
	}
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "trace"},
		{http.MethodGet, traceDataPath},
		{http.MethodDelete, traceDataPath},
	} {
		if got := callManagement(t, probe.method, probe.path).StatusCode; got != http.StatusNotFound {
			t.Errorf("%s %s status = %d，期望 404", probe.method, probe.path, got)
		}
	}

	// 重新打开 trace 后（panel 仍开）管理可达。
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister,
		lifecycleRequest(t, []byte("panel:\n  enabled: true\ntrace_enabled: true\n"))); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if got := callManagement(t, http.MethodGet, "trace").StatusCode; got != http.StatusOK {
		t.Errorf("双开关打开后 GET trace status = %d，期望 200", got)
	}
}

// registerTrace 按 YAML 注册插件；测试需在调用前设置 panelEnabled。
func registerTrace(t *testing.T, yaml string) {
	t.Helper()
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister,
		lifecycleRequest(t, []byte(yaml))); err != nil {
		t.Fatalf("register: %v", err)
	}
	// 直接写缓冲的用例默认已开始记录；记录开关本身的用例会显式关闭。
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	if p != nil {
		p.traces.setRecording(true)
	}
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func callManagementWithBody(t *testing.T, httpMethod, path, body string) pluginapi.ManagementResponse {
	t.Helper()
	payload, _ := json.Marshal(pluginapi.ManagementRequest{Method: httpMethod, Path: path, Body: []byte(body)})
	return callManagementRaw(t, pluginabi.MethodManagementHandle, payload)
}

// 需求 7.6：页面通过 POST .../traces/recording 开始/停止记录；
// 关闭时保留已有记录，开启时清空旧记录。
func TestTraceRecordingEndpoint(t *testing.T) {
	enablePanelForTrace(t)
	registerTrace(t, "panel:\n  enabled: true\ntrace_enabled: true\n")
	abiState.RLock()
	p := abiState.plugin
	abiState.RUnlock()
	p.traces.setRecording(false)

	p.traces.record("r0", "t0", "request.received", nil)
	if n := len(p.traces.snapshot(0, 10, "").Requests); n != 0 {
		t.Fatalf("停止状态不应采集，实际 %d 条", n)
	}
	initial := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	if got := mustBool(t, initial["recording"]); got {
		t.Error("初始 recording 应为 false")
	}
	var plug map[string]json.RawMessage
	_ = json.Unmarshal(initial["plugin"], &plug)
	if got := mustBool(t, plug["trace_recording"]); got {
		t.Error("初始 plugin.trace_recording 应为 false")
	}

	start := callManagementWithBody(t, http.MethodPost, traceRecordingPath, `{"recording":true}`)
	if start.StatusCode != http.StatusOK {
		t.Fatalf("POST recording=true status = %d，期望 200：%s", start.StatusCode, start.Body)
	}
	var started struct {
		Recording bool `json:"recording"`
		Cleared   int  `json:"cleared"`
	}
	if errDecode := json.Unmarshal(start.Body, &started); errDecode != nil {
		t.Fatalf("POST 响应解码失败：%v (%s)", errDecode, start.Body)
	}
	if !started.Recording {
		t.Error("POST recording=true 应返回 recording=true")
	}
	p.traces.record("r1", "t1", "request.received", nil)
	on := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	if got := mustBool(t, on["recording"]); !got {
		t.Error("开启后 GET recording 应为 true")
	}
	var onReqs []map[string]any
	_ = json.Unmarshal(on["requests"], &onReqs)
	if len(onReqs) != 1 || onReqs[0]["request_id"] != "r1" {
		t.Errorf("开启后应采集 r1：%s", on["requests"])
	}

	stop := callManagementWithBody(t, http.MethodPost, traceRecordingPath, `{"recording":false}`)
	if stop.StatusCode != http.StatusOK {
		t.Fatalf("POST recording=false status = %d，期望 200", stop.StatusCode)
	}
	p.traces.record("r2", "t2", "request.received", nil)
	off := traceJSONBody(t, callManagement(t, http.MethodGet, traceDataPath))
	if got := mustBool(t, off["recording"]); got {
		t.Error("停止后 GET recording 应为 false")
	}
	var offReqs []map[string]any
	_ = json.Unmarshal(off["requests"], &offReqs)
	if len(offReqs) != 1 {
		t.Errorf("停止后应保留已有记录且不新增，实际 %d 条", len(offReqs))
	}
}

// 需求 7.6/10：记录开关同样受管理可见性约束，只接受 POST + 合法 JSON。
func TestTraceRecordingEndpointContract(t *testing.T) {
	resetABIState(t)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if got := callManagement(t, method, traceRecordingPath).StatusCode; got != http.StatusNotFound {
			t.Errorf("panel.enabled=false 时 %s recording status = %d，期望 404", method, got)
		}
	}

	enablePanelForTrace(t)
	registerTrace(t, "panel:\n  enabled: true\ntrace_enabled: true\n")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		resp := callManagement(t, method, traceRecordingPath)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s recording status = %d，期望 405", method, resp.StatusCode)
		}
		if got := resp.Headers.Get("Allow"); got != "POST" {
			t.Errorf("%s recording Allow = %q，期望 POST", method, got)
		}
	}
	for _, body := range []string{"", "{}", `{"recording":"yes"}`, "not-json"} {
		if got := callManagementWithBody(t, http.MethodPost, traceRecordingPath, body).StatusCode; got != http.StatusBadRequest {
			t.Errorf("body %q status = %d，期望 400", body, got)
		}
	}
	// 不可通过资源通道操作记录开关。
	if got := callManagement(t, http.MethodPost, "/v0/resource/plugins/privacygateway/traces/recording").StatusCode; got != http.StatusNotFound {
		t.Errorf("资源通道 recording status = %d，期望 404", got)
	}
}

func mustBool(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var v bool
	if errDecode := json.Unmarshal(raw, &v); errDecode != nil {
		t.Fatalf("bool 解码失败：%v (%s)", errDecode, raw)
	}
	return v
}
