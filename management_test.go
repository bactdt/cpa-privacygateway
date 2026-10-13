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

func TestManagementPanelDisabledByDefault(t *testing.T) {
	resetABIState(t)
	resp := callManagement(t, http.MethodGet, "panel")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("面板默认关闭，GET panel status = %d，期望 404", resp.StatusCode)
	}
	if got := resp.Headers.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q，期望 text/plain; charset=utf-8", got)
	}
}

// TestManagementPanelEnabledByConfig 覆盖 panel.enabled: true 的注册路径：
// 注册信封声明 management_api、resources 暴露 panel、GET 返回 HTML。
func TestManagementPanelEnabledByConfig(t *testing.T) {
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
	if len(env.Result.Resources) != 1 || env.Result.Resources[0].Path != "panel" {
		t.Errorf("panel.enabled=true 时应暴露 panel 资源：%s", raw)
	}

	resp := callManagement(t, http.MethodGet, "panel")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("panel.enabled=true 时 GET panel status = %d，期望 200", resp.StatusCode)
	}
	if got := resp.Headers.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q，期望 text/html; charset=utf-8", got)
	}
	if len(resp.Body) == 0 {
		t.Error("panel.enabled=true 时 GET panel 应返回页面正文")
	}

	if got := callManagement(t, http.MethodPost, "panel").StatusCode; got != http.StatusMethodNotAllowed {
		t.Errorf("panel.enabled=true 时 POST panel status = %d，期望 405", got)
	}
	if got := callManagement(t, http.MethodGet, "other").StatusCode; got != http.StatusNotFound {
		t.Errorf("panel.enabled=true 时 GET other status = %d，期望 404", got)
	}
}

// TestManagementPanelFollowsReconfigure 覆盖重配切换 panel.enabled 的路径。
func TestManagementPanelFollowsReconfigure(t *testing.T) {
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
	if got := callManagement(t, http.MethodGet, "panel").StatusCode; got != http.StatusNotFound {
		t.Errorf("默认配置 GET panel status = %d，期望 404", got)
	}
	if caps := register("max_mappings: 50\npanel:\n  enabled: true"); caps["management_api"] != true {
		t.Errorf("panel.enabled=true 时 capabilities.management_api 应为 true：%v", caps)
	}
	if got := callManagement(t, http.MethodGet, "panel").StatusCode; got != http.StatusOK {
		t.Errorf("panel.enabled=true 后 GET panel status = %d，期望 200", got)
	}
	if caps := register("max_mappings: 50"); caps["management_api"] != false {
		t.Errorf("panel.enabled=false 时 capabilities.management_api 应为 false：%v", caps)
	}
	if got := callManagement(t, http.MethodGet, "panel").StatusCode; got != http.StatusNotFound {
		t.Errorf("panel.enabled=false 后 GET panel status = %d，期望 404", got)
	}
}

func TestManagementPathNormalization(t *testing.T) {
	for _, path := range []string{"panel", "/panel", "panel/", "/panel/", "/v0/resource/plugins/privacygateway/panel"} {
		if got := normalizeManagementPath(path); got != "panel" {
			t.Errorf("normalizeManagementPath(%q) = %q，期望 panel", path, got)
		}
	}
	for _, path := range []string{"", "other", "panel/x", "/v0/resource/plugins/other/panel", "../panel"} {
		if got := normalizeManagementPath(path); got == "panel" {
			t.Errorf("normalizeManagementPath(%q) 不应归一化为 panel", path)
		}
	}
}

func TestManagementHandleUnknownPath(t *testing.T) {
	resetABIState(t)
	for _, path := range []string{"", "other", "panel/x", "/v0/resource/plugins/other/panel", "../panel"} {
		if got := callManagement(t, http.MethodGet, path).StatusCode; got != http.StatusNotFound {
			t.Errorf("路径 %q status = %d，期望 404", path, got)
		}
	}
}

func TestManagementHandleRejectsNonGET(t *testing.T) {
	resetABIState(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, ""} {
		resp := callManagement(t, method, "panel")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("面板默认关闭，方法 %q status = %d，期望 404", method, resp.StatusCode)
		}
	}
}

func TestManagementHandleUndecodableRequest(t *testing.T) {
	resetABIState(t)
	resp := callManagementRaw(t, pluginabi.MethodManagementHandle, []byte(`{bad`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("面板默认关闭，status = %d，期望 404（关闭检查先于请求解码）", resp.StatusCode)
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
	if got := callManagement(t, http.MethodGet, "panel").StatusCode; got != http.StatusNotFound {
		t.Errorf("未注册时 panel status = %d，期望 404（面板默认关闭）", got)
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
	raw, err := registrationEnvelope()
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

func TestEmbeddedPanelMatchesFile(t *testing.T) {
	onDisk, err := os.ReadFile("packaging/panel.html")
	if err != nil {
		t.Fatalf("读取 packaging/panel.html 失败：%v", err)
	}
	if !bytes.Equal(onDisk, panelHTML) {
		t.Error("嵌入的 panelHTML 与 packaging/panel.html 不一致（需重新编译）")
	}
	if !bytes.HasPrefix(bytes.ToLower(bytes.TrimSpace(panelHTML)), []byte("<!doctype html")) {
		t.Error("panelHTML 应以 <!doctype html 开头")
	}
}

func TestManagementResponseBodyIsBase64(t *testing.T) {
	resetABIState(t)
	payload, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "panel"})
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
