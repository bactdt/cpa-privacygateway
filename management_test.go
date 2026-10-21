package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func callManagementRaw(t *testing.T, method string, payload []byte) pluginapi.ManagementResponse {
	t.Helper()
	raw, err := handleABIMethod(context.Background(), method, payload)
	if err != nil {
		t.Fatalf("handleABIMethod 返回 error：%v", err)
	}
	var env abiEnvelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil {
		t.Fatalf("信封解码失败：%v", errDecode)
	}
	if !env.OK {
		t.Fatalf("信封 ok=false：%s", raw)
	}
	var resp pluginapi.ManagementResponse
	if errDecode := json.Unmarshal(env.Result, &resp); errDecode != nil {
		t.Fatalf("ManagementResponse 解码失败：%v", errDecode)
	}
	return resp
}

func callManagement(t *testing.T, httpMethod, path string) pluginapi.ManagementResponse {
	t.Helper()
	payload, _ := json.Marshal(pluginapi.ManagementRequest{Method: httpMethod, Path: path})
	return callManagementRaw(t, pluginabi.MethodManagementHandle, payload)
}

func TestManagementRegisterResponseShape(t *testing.T) {
	resetABIState(t)
	raw, err := handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env struct {
		OK     bool                       `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("信封异常：%s (%v)", raw, errDecode)
	}
	if got := string(env.Result["routes"]); got != "[]" {
		t.Errorf("routes 必须编码为空数组，实际 %q", got)
	}
	var resources []map[string]any
	if errDecode := json.Unmarshal(env.Result["resources"], &resources); errDecode != nil || len(resources) != 0 {
		t.Errorf("面板默认关闭，resources 应为空数组：%s", env.Result["resources"])
	}
}

func TestManagementDisabledByDefault(t *testing.T) {
	resetABIState(t)
	for _, path := range []string{"trace", "traces"} {
		resp := callManagement(t, http.MethodGet, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("panel.enabled 默认关闭，GET %s status = %d，期望 404", path, resp.StatusCode)
		}
		if got := resp.Headers.Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q，期望 text/plain; charset=utf-8", got)
		}
	}
}

// TestManagementEnabledByConfig 覆盖 panel.enabled: true 的注册路径：
// 注册信封声明 management_api、resources 暴露 trace、GET 返回 HTML。
func TestManagementEnabledByConfig(t *testing.T) {
	resetABIState(t)
	abiState.Lock()
	abiState.panelEnabled = true
	abiState.Unlock()
	t.Cleanup(func() {
		abiState.Lock()
		abiState.panelEnabled = false
		abiState.Unlock()
	})

	raw, err := handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Resources []struct {
				Path string `json:"Path"`
				Menu string `json:"Menu"`
			} `json:"resources"`
		} `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("信封异常：%s", raw)
	}
	if len(env.Result.Resources) != 1 || env.Result.Resources[0].Path != "trace" {
		t.Errorf("panel.enabled=true 时应暴露 trace 资源：%s", raw)
	}

	resp := callManagement(t, http.MethodGet, "trace")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("panel.enabled=true 时 GET trace status = %d，期望 200", resp.StatusCode)
	}
	if got := resp.Headers.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q，期望 text/html; charset=utf-8", got)
	}
	if len(resp.Body) == 0 {
		t.Error("panel.enabled=true 时 GET trace 应返回页面正文")
	}

	if got := callManagement(t, http.MethodPost, "trace").StatusCode; got != http.StatusMethodNotAllowed {
		t.Errorf("panel.enabled=true 时 POST trace status = %d，期望 405", got)
	}
	if got := callManagement(t, http.MethodGet, "other").StatusCode; got != http.StatusNotFound {
		t.Errorf("panel.enabled=true 时 GET other status = %d，期望 404", got)
	}
}

// TestManagementFollowsReconfigure 覆盖重配切换 panel.enabled 的路径。
func TestManagementFollowsReconfigure(t *testing.T) {
	resetABIState(t)
	register := func(yaml string) map[string]bool {
		t.Helper()
		raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(yaml)})
		rawResp, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw)
		if err != nil {
			t.Fatalf("register(%q): %v", yaml, err)
		}
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				Capabilities map[string]bool `json:"capabilities"`
			} `json:"result"`
		}
		if errDecode := json.Unmarshal(rawResp, &env); errDecode != nil || !env.OK {
			t.Fatalf("register(%q) 信封异常：%s", yaml, rawResp)
		}
		return env.Result.Capabilities
	}
	register("max_mappings: 50")
	if got := callManagement(t, http.MethodGet, "trace").StatusCode; got != http.StatusNotFound {
		t.Errorf("默认配置 GET trace status = %d，期望 404", got)
	}
	if caps := register("max_mappings: 50\npanel:\n  enabled: true"); caps["management_api"] != true {
		t.Errorf("panel.enabled=true 时 capabilities.management_api 应为 true：%v", caps)
	}
	if got := callManagement(t, http.MethodGet, "trace").StatusCode; got != http.StatusOK {
		t.Errorf("panel.enabled=true 后 GET trace status = %d，期望 200", got)
	}
	if caps := register("max_mappings: 50"); caps["management_api"] != false {
		t.Errorf("panel.enabled=false 时 capabilities.management_api 应为 false：%v", caps)
	}
	if got := callManagement(t, http.MethodGet, "trace").StatusCode; got != http.StatusNotFound {
		t.Errorf("panel.enabled=false 后 GET trace status = %d，期望 404", got)
	}
}

func TestManagementPathNormalization(t *testing.T) {
	for _, path := range []string{"trace", "/trace", "trace/", "/trace/", "/v0/resource/plugins/privacygateway/trace"} {
		if got := normalizeManagementPath(path); got != "trace" {
			t.Errorf("normalizeManagementPath(%q) = %q，期望 trace", path, got)
		}
	}
	for _, path := range []string{"", "other", "trace/x", "/v0/resource/plugins/other/trace", "../trace"} {
		if got := normalizeManagementPath(path); got == "trace" {
			t.Errorf("normalizeManagementPath(%q) 不应归一化为 trace", path)
		}
	}
	for _, path := range []string{"traces", "/traces", "traces/", "privacygateway/traces", "/v0/management/privacygateway/traces"} {
		if got := normalizeManagementPath(path); got != "traces" && got != "privacygateway/traces" && got != "v0/management/privacygateway/traces" {
			t.Errorf("normalizeManagementPath(%q) = %q，不应误折叠为 trace", path, got)
		}
	}
}

func TestManagementHandleUnknownPath(t *testing.T) {
	resetABIState(t)
	for _, path := range []string{"", "other", "trace/x", "/v0/resource/plugins/other/trace", "../trace"} {
		if got := callManagement(t, http.MethodGet, path).StatusCode; got != http.StatusNotFound {
			t.Errorf("路径 %q status = %d，期望 404", path, got)
		}
	}
}

func TestManagementHandleRejectsNonGETOnPage(t *testing.T) {
	resetABIState(t)
	abiState.Lock()
	abiState.panelEnabled = true
	abiState.Unlock()
	t.Cleanup(func() { resetABIState(t) })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, ""} {
		resp := callManagement(t, method, "trace")
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("trace 页方法 %q status = %d，期望 405", method, resp.StatusCode)
		}
		if got := resp.Headers.Get("Allow"); got != "GET" {
			t.Errorf("trace 页 Allow = %q，期望 GET", got)
		}
	}
}

func TestManagementHandleUndecodableRequest(t *testing.T) {
	resetABIState(t)
	resp := callManagementRaw(t, pluginabi.MethodManagementHandle, []byte(`{bad`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("解码失败 status = %d，期望 400", resp.StatusCode)
	}
}

func TestManagementRefusedAfterShutdown(t *testing.T) {
	resetABIState(t)
	abiState.Lock()
	abiState.shuttingDown = true
	abiState.Unlock()
	t.Cleanup(func() { resetABIState(t) })
	payload, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "panel"})
	for _, method := range []string{pluginabi.MethodManagementRegister, pluginabi.MethodManagementHandle} {
		raw, err := handleABIMethod(context.Background(), method, payload)
		if err == nil {
			var env abiEnvelope
			_ = json.Unmarshal(raw, &env)
			if env.OK {
				t.Errorf("%s 在 shutdown 后不应成功：%s", method, raw)
			}
		}
	}
}

func TestManagementIndependentOfRegistration(t *testing.T) {
	resetABIState(t)
	abiState.RLock()
	registered := abiState.plugin != nil
	abiState.RUnlock()
	if registered {
		t.Fatal("前置条件：插件应处于未注册状态")
	}
	if got := callManagement(t, http.MethodGet, "trace").StatusCode; got != http.StatusNotFound {
		t.Errorf("未注册时 trace status = %d，期望 404（面板默认关闭）", got)
	}
	raw, err := handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("未注册时 management.register 失败：%v", err)
	}
	var env abiEnvelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Errorf("未注册时 management.register 应成功：%s", raw)
	}
}

func TestRegistrationDoesNotDeclareManagementAPI(t *testing.T) {
	raw, err := registrationEnvelope(false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("信封异常：%s", raw)
	}
	if env.Result.Capabilities["management_api"] {
		t.Errorf("面板默认关闭，capabilities.management_api 不应声明为 true：%v", env.Result.Capabilities)
	}
	for _, key := range []string{"request_interceptor", "request_lifecycle_plugin", "response_interceptor", "response_stream_interceptor"} {
		if !env.Result.Capabilities[key] {
			t.Errorf("原有能力 %s 不应丢失", key)
		}
	}
}

func TestEmbeddedTraceMatchesFile(t *testing.T) {
	onDisk, err := os.ReadFile("packaging/trace.html")
	if err != nil {
		t.Fatalf("读取 packaging/trace.html 失败：%v", err)
	}
	if !bytes.Equal(onDisk, traceHTML) {
		t.Error("嵌入的 traceHTML 与 packaging/trace.html 不一致（需重新编译）")
	}
	if !bytes.HasPrefix(bytes.ToLower(bytes.TrimSpace(traceHTML)), []byte("<!doctype html")) {
		t.Error("traceHTML 应以 <!doctype html 开头")
	}
}

func TestManagementResponseBodyIsBase64(t *testing.T) {
	resetABIState(t)
	abiState.Lock()
	abiState.panelEnabled = true
	abiState.Unlock()
	t.Cleanup(func() { resetABIState(t) })
	payload, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "trace"})
	raw, err := handleABIMethod(context.Background(), pluginabi.MethodManagementHandle, payload)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil {
		t.Fatalf("解码失败：%v", errDecode)
	}
	var body string
	if errDecode := json.Unmarshal(env.Result["Body"], &body); errDecode != nil {
		t.Fatalf("Body 应编码为 JSON 字符串（[]byte 的 Base64）：%s", env.Result["Body"])
	}
	if len(body) == 0 || bytes.Contains([]byte(body), []byte("<!doctype")) {
		t.Error("Body 应为 Base64 文本，而非明文 HTML")
	}
}

// TestPanelDisabledAfterShutdownAndReinit 覆盖生命周期清理：shutdown 与
// 重新 init 都必须清除上一轮注册遗留的 panel.enabled，未重新 register
// 前面板保持关闭。
func TestPanelDisabledAfterShutdownAndReinit(t *testing.T) {
	resetABIState(t)
	raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("panel:\n  enabled: true")})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatalf("register: %v", err)
	}
	abiState.RLock()
	enabled := abiState.panelEnabled
	abiState.RUnlock()
	if !enabled {
		t.Fatal("前置条件：注册 panel.enabled=true 后 abiState.panelEnabled 应为 true")
	}

	PrivacyGatewayPluginShutdown()
	if panelEnabled() {
		t.Error("shutdown 后 panelEnabled 应被清除为 false")
	}

	// 重新 init 走同一重置路径（cgo 导出函数无法在测试中直接调用，
	// 这里验证其共享的 resetPanelEnabledLocked 语义）。
	abiState.Lock()
	abiState.panelEnabled = true
	resetPanelEnabledLocked()
	abiState.Unlock()
	if panelEnabled() {
		t.Error("重新 init 路径重置后未重新 register，panelEnabled 应保持 false")
	}
}
