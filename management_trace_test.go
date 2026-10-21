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
	for _, path := range []string{"trace", "traces"} {
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
	payload := traceJSONBody(t, callManagement(t, http.MethodGet, "traces"))
	for _, key := range []string{"version", "plugin", "limits", "buffered", "evicted", "next_since", "requests"} {
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
	sinceQuery := "traces?since=" + itoa64(int64(sinceSeq))
	filtered := traceJSONBody(t, callManagement(t, http.MethodGet, sinceQuery))
	var after []map[string]any
	_ = json.Unmarshal(filtered["requests"], &after)
	if len(after) != 0 {
		t.Errorf("since=最新序号后应返回 0 条，实际 %d", len(after))
	}

	// limit=1 截断。
	limited := traceJSONBody(t, callManagement(t, http.MethodGet, "traces?limit=1"))
	var one []map[string]any
	_ = json.Unmarshal(limited["requests"], &one)
	if len(one) != 1 {
		t.Errorf("limit=1 应返回 1 条，实际 %d", len(one))
	}

	// 非法 limit 回退默认 50（此处 2 条全部返回）。
	invalid := traceJSONBody(t, callManagement(t, http.MethodGet, "traces?limit=abc&limit=99999"))
	var invalidReqs []map[string]any
	_ = json.Unmarshal(invalid["requests"], &invalidReqs)
	if len(invalidReqs) != 2 {
		t.Errorf("非法 limit 应回退默认，返回 %d 条，期望 2", len(invalidReqs))
	}

	// filter=findings：只保留带 finding 的记录。
	findings := traceJSONBody(t, callManagement(t, http.MethodGet, "traces?filter=findings"))
	var fReqs []map[string]any
	_ = json.Unmarshal(findings["requests"], &fReqs)
	if len(fReqs) != 1 || fReqs[0]["request_id"] != "r2" {
		t.Errorf("filter=findings 应只返回 r2，实际 %s", findings["requests"])
	}
	// filter 非法值回退 all。
	all := traceJSONBody(t, callManagement(t, http.MethodGet, "traces?filter=bogus"))
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

	deleted := traceJSONBody(t, callManagement(t, http.MethodDelete, "traces"))
	var buffered int
	if errDecode := json.Unmarshal(deleted["buffered"], &buffered); errDecode != nil {
		t.Fatalf("buffered 解码失败：%v", errDecode)
	}
	if buffered != 0 {
		t.Errorf("DELETE 后 buffered = %d，期望 0", buffered)
	}
	if got := len(p.traces.snapshot(0, 10, "").Requests); got != 0 {
		t.Errorf("DELETE 后仍剩 %d 条记录", got)
	}
	// DELETE 后 GET 返回空数组。
	empty := traceJSONBody(t, callManagement(t, http.MethodGet, "traces"))
	var reqs []map[string]any
	_ = json.Unmarshal(empty["requests"], &reqs)
	if len(reqs) != 0 {
		t.Errorf("清空后 GET 返回 %d 条，期望 0", len(reqs))
	}
}

func TestTraceDataRejectsUnsupportedMethod(t *testing.T) {
	enablePanelForTrace(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		resp := callManagement(t, method, "traces")
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s traces status = %d，期望 405", method, resp.StatusCode)
		}
		if got := resp.Headers.Get("Allow"); !strings.Contains(got, "GET") || !strings.Contains(got, "DELETE") {
			t.Errorf("Allow = %q，期望含 GET/DELETE", got)
		}
	}
}

func TestTraceDataWithoutPluginReturnsEmptySnapshot(t *testing.T) {
	enablePanelForTrace(t)
	payload := traceJSONBody(t, callManagement(t, http.MethodGet, "traces"))
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

// registerTrace 按 YAML 注册插件；测试需在调用前设置 panelEnabled。
func registerTrace(t *testing.T, yaml string) {
	t.Helper()
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister,
		lifecycleRequest(t, []byte(yaml))); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
