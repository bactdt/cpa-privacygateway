package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"duanjuapp/cpa/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func resetABIState(t *testing.T) {
	t.Helper()
	abiState.Lock()
	abiState.plugin = nil
	abiState.shuttingDown = false
	abiState.Unlock()
}

func TestHandleABIRegister(t *testing.T) {
	resetABIState(t)
	req := abiLifecycleRequest{ConfigYAML: []byte(`max_mappings: 50`)}
	raw, _ := json.Marshal(req)
	resp, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw)
	if err != nil {
		t.Fatalf("handleABIRegister: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if !env.OK {
		t.Fatalf("register 应成功：%s", resp)
	}
	if abiState.plugin == nil {
		t.Error("plugin 应已注册")
	}
}

func TestHandleABIReconfigure(t *testing.T) {
	resetABIState(t)
	req := abiLifecycleRequest{ConfigYAML: []byte(`max_mappings: 50`)}
	raw, _ := json.Marshal(req)
	_, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	newCfg := []byte(`max_mappings: 100`)
	raw2, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: newCfg})
	_, err = handleABIRegister(context.Background(), pluginabi.MethodPluginReconfigure, raw2)
	if err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	if abiState.plugin == nil {
		t.Fatal("plugin 应存在")
	}
}

func TestHandleABIMethodUnknown(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{})
	_, _ = handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	resp, err := handleABIMethod(context.Background(), "nonexistent.method", []byte(`{}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if env.OK {
		t.Error("未知方法应返回错误")
	}
	if env.Error == nil || !strings.Contains(env.Error.Code, "unknown_method") {
		t.Errorf("错误码 = %v", env.Error)
	}
}

func TestHandleABIMethodNotRegistered(t *testing.T) {
	resetABIState(t)
	req := pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"model":"gpt-4"}`),
	}
	raw, _ := json.Marshal(req)
	// TD-CPA-044：未注册属插件不可用，fail_closed 默认生效，应返回终止信封而非 error。
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)
	if err != nil {
		t.Fatalf("未注册应返回终止信封而非 error：%v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if !ri.Terminate || ri.StatusCode != http.StatusInternalServerError {
		t.Errorf("应 500 终止，实际 terminate=%v status=%d", ri.Terminate, ri.StatusCode)
	}
	if !strings.Contains(string(ri.ResponseBody), "server_error") {
		t.Errorf("应返回 server_error 错误体：%s", ri.ResponseBody)
	}
}

func TestHandleABIRequestInterceptBefore(t *testing.T) {
	resetABIState(t)
	cfgReq := abiLifecycleRequest{ConfigYAML: []byte(``)}
	cfgRaw, _ := json.Marshal(cfgReq)
	_, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"key sk-abcdefghijklmnop1234"}]}`)
	req := pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	}
	raw, _ := json.Marshal(req)
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if !env.OK {
		t.Fatalf("intercept 应成功：%s", resp)
	}
	var ri pluginapi.RequestInterceptResponse
	json.Unmarshal(env.Result, &ri)
	if strings.Contains(string(ri.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("敏感数据应被脱敏：%s", ri.Body)
	}
}

func TestHandleABIRequestComplete(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{})
	_, _ = handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	reqBody := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	riq := pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         reqBody,
	}
	raw, _ := json.Marshal(riq)
	_, _ = handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)
	completion := pluginapi.RequestCompletion{RequestID: "req-1"}
	cRaw, _ := json.Marshal(completion)
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestComplete, cRaw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if !env.OK {
		t.Errorf("complete 应成功：%s", resp)
	}
}

func TestHandleABIResponseIntercept(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{})
	_, _ = handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	body := []byte(`{"messages":[{"role":"user","content":"my secret is sk-abcdefghijklmnop1234"}]}`)
	riq := pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	}
	raw, _ := json.Marshal(riq)
	redactResp, _ := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)
	var env abiEnvelope
	json.Unmarshal(redactResp, &env)
	var ri pluginapi.RequestInterceptResponse
	json.Unmarshal(env.Result, &ri)

	// Response with placeholder -> should restore
	respReq := pluginapi.ResponseInterceptRequest{
		RequestID: "req-1",
		Body:      ri.Body,
	}
	respRaw, _ := json.Marshal(respReq)
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodResponseInterceptAfter, respRaw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env2 abiEnvelope
	json.Unmarshal(resp, &env2)
	if !env2.OK {
		t.Fatalf("response intercept 应成功：%s", resp)
	}
	var ri2 pluginapi.ResponseInterceptResponse
	json.Unmarshal(env2.Result, &ri2)
	if !strings.Contains(string(ri2.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("响应应还原：%s", ri2.Body)
	}
}

func TestHandleABIStreamChunk(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{})
	_, _ = handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	body := []byte(`{"messages":[{"role":"user","content":"my secret is sk-abcdefghijklmnop1234"}]}`)
	riq := pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	}
	raw, _ := json.Marshal(riq)
	handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)

	mapping := abiState.plugin.states.mapping("req-1")
	token, _ := mapping.Placeholder("sk-abcdefghijklmnop1234")
	// 宿主形态：裸 JSON chunk，占位符跨两个 chunk 切开。
	chunk1 := []byte(`{"choices":[{"index":0,"delta":{"content":"key: ` + token[:len(token)/2] + `"}}]}`)
	chunk2 := []byte(`{"choices":[{"index":0,"delta":{"content":"` + token[len(token)/2:] + ` done"}}]}`)
	var combined strings.Builder
	for i, chunk := range [][]byte{chunk1, chunk2} {
		sc := pluginapi.StreamChunkInterceptRequest{
			RequestID:    "req-1",
			SourceFormat: "openai",
			ChunkIndex:   i,
			Body:         chunk,
		}
		scRaw, _ := json.Marshal(sc)
		resp, err := handleABIMethod(context.Background(), pluginabi.MethodResponseInterceptStreamChunk, scRaw)
		if err != nil {
			t.Fatalf("chunk %d err: %v", i, err)
		}
		var env abiEnvelope
		if err := json.Unmarshal(resp, &env); err != nil || !env.OK {
			t.Fatalf("chunk %d 应成功：%s", i, resp)
		}
		var sr pluginapi.StreamChunkInterceptResponse
		if err := json.Unmarshal(env.Result, &sr); err != nil {
			t.Fatalf("chunk %d 解码失败：%v", i, err)
		}
		if sr.DropChunk {
			t.Fatalf("chunk %d 不应被丢弃", i)
		}
		effective := chunk
		if len(sr.Body) > 0 {
			effective = sr.Body
		}
		if !json.Valid(effective) {
			t.Fatalf("chunk %d 输出应为合法 JSON：%s", i, effective)
		}
		var payload struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(effective, &payload); err != nil {
			t.Fatalf("chunk %d 解析失败：%v", i, err)
		}
		for _, c := range payload.Choices {
			combined.WriteString(c.Delta.Content)
		}
	}
	got := combined.String()
	if !strings.Contains(got, "sk-abcdefghijklmnop1234") {
		t.Errorf("流式 chunk 应还原：%s", got)
	}
	if strings.Contains(got, "[[PG_") {
		t.Errorf("不应残留占位符：%s", got)
	}
}

func TestPluginMetadata(t *testing.T) {
	meta := pluginMetadata()
	if meta.Name != "privacygateway" {
		t.Errorf("Name = %q", meta.Name)
	}
	if len(meta.ConfigFields) == 0 {
		t.Error("应有配置字段")
	}
}

func TestBeginPluginCallNotRegistered(t *testing.T) {
	resetABIState(t)
	_, _, err := beginPluginCall()
	if err == nil {
		t.Error("未注册时应报错")
	}
}

func TestBeginPluginCallShuttingDown(t *testing.T) {
	resetABIState(t)
	abiState.Lock()
	abiState.plugin = newPlugin(defaultConfig())
	abiState.shuttingDown = true
	abiState.Unlock()
	_, _, err := beginPluginCall()
	if err == nil {
		t.Error("shutting down 时应报错")
	}
	resetABIState(t)
}

func TestHostCallbackIDContext(t *testing.T) {
	ctx := context.Background()
	if hostCallbackIDFromContext(ctx) != "" {
		t.Error("空 context 应返回空")
	}
	ctx = withHostCallbackID(ctx, "cb-123")
	if hostCallbackIDFromContext(ctx) != "cb-123" {
		t.Error("应返回 cb-123")
	}
}

func TestLogHostNilHost(t *testing.T) {
	resetABIState(t)
	logHost(context.Background(), logLevelInfo, "test", nil)
}

func TestAbiEnvelopeOK(t *testing.T) {
	raw, err := abiOKEnvelope(map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(raw, &env)
	if !env.OK {
		t.Error("OK 应为 true")
	}
}

func TestAbiEnvelopeError(t *testing.T) {
	raw := abiErrorEnvelope("test_code", "test message")
	var env abiEnvelope
	json.Unmarshal(raw, &env)
	if env.OK {
		t.Error("OK 应为 false")
	}
	if env.Error == nil || env.Error.Code != "test_code" {
		t.Errorf("Error = %v", env.Error)
	}
}

var _ = redact.KindEmail

func TestAbiOKEnvelopeWithError(t *testing.T) {
	raw, err := abiOKEnvelopeWithError(nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(raw, &env)
	if !env.OK {
		t.Error("OK 应为 true")
	}

	_, err = abiOKEnvelopeWithError(nil, &testError{})
	if err == nil {
		t.Error("有错误时应返回错误")
	}
}

type testError struct{}

func (e *testError) Error() string { return "test error" }

func TestHandleABIMethodRegisterBadConfig(t *testing.T) {
	resetABIState(t)
	raw := []byte(`{"config_yaml": "invalid: yaml: ["}`)
	_, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw)
	if err == nil {
		t.Error("无效配置应报错")
	}
}

func TestHandleABIRegisterReconfigureBadConfigKeepsPrevious(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(`max_mappings: 50`)})
	_, _ = handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw)
	badRaw := []byte(`{"config_yaml": "aW52YWxpZDogeWFtbDogWyI="}`)
	resp, err := handleABIRegister(context.Background(), pluginabi.MethodPluginReconfigure, badRaw)
	if err != nil {
		t.Fatalf("reconfigure 失败应保留旧配置：%v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if !env.OK {
		t.Error("reconfigure 失败时应返回 OK 并保留旧配置")
	}
}

func TestPluginInterceptStreamChunkHeaderInit(t *testing.T) {
	p := newPlugin(defaultConfig())
	resp, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		RequestID:  "req-1",
		ChunkIndex: pluginapi.StreamChunkHeaderInitIndex,
		Body:       []byte("data"),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.DropChunk {
		t.Error("头部初始化 chunk 不应 drop")
	}
}

func TestPluginInterceptStreamChunkNoMapping(t *testing.T) {
	p := newPlugin(defaultConfig())
	resp, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		RequestID:  "nonexistent",
		ChunkIndex: 0,
		Body:       []byte("data"),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Body != nil {
		t.Error("无 mapping 时应返回 nil body")
	}
}

func TestPluginInterceptRequestBeforeAuthBodyTooLarge(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxBodyBytes = 10
	p := newPlugin(cfg)
	body := []byte(`{"messages":[{"role":"user","content":"this is way too long"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("超限 body 在 fail_closed 下应 terminate")
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
}

func TestPluginInterceptRequestBeforeAuthBodyTooLargePassThrough(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxBodyBytes = 10
	fc := false
	cfg.FailClosed = &fc
	p := newPlugin(cfg)
	body := []byte(`{"messages":[{"role":"user","content":"this is way too long"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("fail_closed=false 超限时不应 terminate")
	}
}

func TestPluginInterceptRequestBeforeAuthMalformedJSON(t *testing.T) {
	p := newPlugin(defaultConfig())
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{invalid json`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("畸形 JSON 在 fail_closed 下应 terminate")
	}
}

func TestPluginInterceptRequestBeforeAuthNoSensitive(t *testing.T) {
	p := newPlugin(defaultConfig())
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("无敏感内容不应 terminate")
	}
	if string(resp.Body) != string(body) {
		t.Errorf("无敏感内容应原样返回")
	}
}

func TestParseConfigInvalidMaxRequests(t *testing.T) {
	_, err := parseConfig([]byte(`max_requests: 0`))
	if err == nil {
		t.Error("max_requests=0 应报错")
	}
}

func TestParseConfigInvalidMaxBodyBytes(t *testing.T) {
	_, err := parseConfig([]byte(`max_body_bytes: 0`))
	if err == nil {
		t.Error("max_body_bytes=0 应报错")
	}
}

func TestParseConfigInvalidMaxConcurrency(t *testing.T) {
	_, err := parseConfig([]byte(`max_concurrency: -1`))
	if err == nil {
		t.Error("max_concurrency=-1 应报错")
	}
}

func TestParseConfigInvalidMinEntropy(t *testing.T) {
	_, err := parseConfig([]byte("detectors:\n  min_entropy: -1\n"))
	if err == nil {
		t.Error("min_entropy=-1 应报错")
	}
}

func TestParseConfigFailClosedFalse(t *testing.T) {
	cfg, err := parseConfig([]byte(`fail_closed: false`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.failClosed() {
		t.Error("fail_closed=false 应返回 false")
	}
}

func TestFailClosedNil(t *testing.T) {
	cfg := config{}
	if !cfg.failClosed() {
		t.Error("FailClosed=nil 应默认 true")
	}
}

var _ = redact.KindEmail

func mustABIRequest(t *testing.T, id, body string) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID:    id,
		SourceFormat: "openai",
		Body:         []byte(body),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestHandleABIRegisterDisabledYAML(t *testing.T) {
	resetABIState(t)
	raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(`enabled: false`)})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatalf("register: %v", err)
	}
	if abiState.plugin == nil {
		t.Fatal("plugin 应存在")
	}
	if abiState.plugin.cfg.Enabled {
		t.Error("enabled: false 应生效")
	}
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, mustABIRequest(t, "req-1", `{"content":"sk-abcdefghijklmnop1234"}`))
	if err != nil {
		t.Fatalf("intercept: %v", err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	json.Unmarshal(resp, &env)
	var body struct {
		Body []byte `json:"Body"`
	}
	json.Unmarshal(env.Result, &body)
	if !bytes.Contains(body.Body, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("禁用后应原文放行：%s", body.Body)
	}
}

func TestHandleABIReconfigureMaxFindings(t *testing.T) {
	resetABIState(t)
	raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(`max_findings: 1`)})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatalf("register: %v", err)
	}
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, mustABIRequest(t, "req-1", `{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih"}`))
	if err != nil {
		t.Fatalf("intercept: %v", err)
	}
	var env abiEnvelope
	json.Unmarshal(resp, &env)
	if !env.OK {
		t.Fatalf("拦截调用不应报协议错误：%+v", env.Error)
	}
	var result pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("解码拦截结果: %v（%s）", err, env.Result)
	}
	if !result.Terminate {
		t.Fatalf("max_findings=1 且正文含 2 处凭据应终止拒绝，实际响应：%s", env.Result)
	}
	if result.StatusCode != http.StatusInternalServerError {
		t.Errorf("应 500，实际 %d", result.StatusCode)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	json.Unmarshal(result.ResponseBody, &payload)
	if payload.Error.Type != "max_findings_exceeded" {
		t.Errorf("应返回 max_findings_exceeded：%s", result.ResponseBody)
	}
	if bytes.Contains(result.ResponseBody, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("拒绝响应不得包含原文：%s", result.ResponseBody)
	}
}

func TestHandleABIShutdownGuardsAllMethods(t *testing.T) {
	resetABIState(t)
	raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(`max_mappings: 50`)})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatalf("register: %v", err)
	}
	PrivacyGatewayPluginShutdown()
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		if _, err := handleABIMethod(context.Background(), method, mustABIRequest(t, "req-x", `{}`)); err == nil {
			t.Errorf("shutdown 后 %s 应被拒绝", method)
		}
	}
	// 请求拦截方法：fail_closed（默认）下 shutdown 后返回终止信封而非 error。
	for _, method := range []string{pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter} {
		resp, err := handleABIMethod(context.Background(), method, mustABIRequest(t, "req-x", `{}`))
		if err != nil {
			t.Errorf("shutdown 后 %s 应返回终止信封而非 error：%v", method, err)
			continue
		}
		ri := decodeTerminatedRequest(t, resp)
		if !ri.Terminate || ri.StatusCode != http.StatusInternalServerError {
			t.Errorf("shutdown 后 %s 应 500 终止，实际 terminate=%v status=%d", method, ri.Terminate, ri.StatusCode)
		}
	}
	// 响应/流/完成方法无终止协议：shutdown 后返回空直通结果而非 error
	// （request.complete 仅是清理通知，空结果无害）。
	for _, method := range []string{pluginabi.MethodResponseInterceptAfter, pluginabi.MethodResponseInterceptStreamChunk, pluginabi.MethodRequestComplete} {
		resp, err := handleABIMethod(context.Background(), method, mustABIRequest(t, "req-x", `{}`))
		if err != nil {
			t.Errorf("shutdown 后 %s 应返回空直通信封而非 error：%v", method, err)
			continue
		}
		var env abiEnvelope
		if err := json.Unmarshal(resp, &env); err != nil || !env.OK {
			t.Errorf("shutdown 后 %s 应返回 OK 空信封：%s（%v）", method, resp, err)
		}
	}
}

func TestParseConfigErrorDoesNotLeakValues(t *testing.T) {
	resetABIState(t)
	sample := "secret-token-abc"
	raw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("keywords:\n  - " + sample + "\nmax_requests: notanumber\n")})
	_, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, raw)
	if err == nil {
		t.Fatal("无效 YAML 应报错")
	}
	if bytes.Contains([]byte(err.Error()), []byte(sample)) {
		t.Errorf("错误信息泄露样例值：%s", err.Error())
	}
	var cfgErr *configParseError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("应包装为 configParseError：%T", err)
	}
}

func TestParseConfigErrorKeepsLineAndCategory(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: notanumber\n"))
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line 1") || !strings.Contains(msg, "cannot unmarshal") {
		t.Errorf("错误应保留行号与类别：%s", msg)
	}
	if strings.Contains(msg, "notanumber") || strings.Contains(msg, "`") {
		t.Errorf("错误不应携带值片段：%s", msg)
	}
}

func TestParseConfigErrorSanitizesBacktickInValue(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: \"ab`SECRETXYZ\"\n"))
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET") || strings.Contains(msg, "`") {
		t.Errorf("反引号值片段应整体剥除：%s", msg)
	}
	if !strings.Contains(msg, "value redacted") || !strings.Contains(msg, "line 1") {
		t.Errorf("应保留类别与行号：%s", msg)
	}
}

func TestParseConfigErrorSanitizesIntoInValue(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: \"ab`x into y`SECRETDD\"\n"))
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET") || strings.Contains(msg, "x in") || strings.Contains(msg, "`") {
		t.Errorf("含 into/反引号的值应整体剥除：%s", msg)
	}
}

func TestParseConfigErrorSanitizesMultipleSegments(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: notanumber\nfail_closed: notabool\n"))
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	if strings.Contains(msg, "notanumber") || strings.Contains(msg, "notabool") {
		t.Errorf("多段错误均应剥值：%s", msg)
	}
	if got := strings.Count(msg, "value redacted"); got != 2 {
		t.Errorf("应剥除 2 段，实际 %d：%s", got, msg)
	}
}

func TestParseConfigErrorSanitizesBinaryTag(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: !!binary aGVsbG8=\n"))
	if err == nil {
		t.Fatal("应报错")
	}
	if msg := err.Error(); strings.Contains(msg, "aGVsbG8=") {
		t.Errorf("binary 值应脱敏：%s", msg)
	}
}

func TestParseConfigErrorUnwrapOriginal(t *testing.T) {
	_, err := parseConfig([]byte("max_requests: notanumber\n"))
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		t.Fatalf("应可 Unwrap 到 *yaml.TypeError：%T", err)
	}
}

// decodeTerminatedRequest 解码拦截方法的 OK 信封结果为 RequestInterceptResponse。
func decodeTerminatedRequest(t *testing.T, raw []byte) pluginapi.RequestInterceptResponse {
	t.Helper()
	var env abiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解码信封: %v（%s）", err, raw)
	}
	if !env.OK {
		t.Fatalf("应返回 OK 信封而非错误信封：%s", raw)
	}
	var resp pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("解码拦截结果: %v（%s）", err, env.Result)
	}
	return resp
}

func TestABINotRegisteredFailClosedTerminates(t *testing.T) {
	resetABIState(t)
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, mustABIRequest(t, "req-1", `{"content":"hi"}`))
	if err != nil {
		t.Fatalf("fail_closed 下未注册应返回终止信封而非 error：%v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if !ri.Terminate || ri.StatusCode != http.StatusInternalServerError {
		t.Errorf("应 500 终止，实际 terminate=%v status=%d", ri.Terminate, ri.StatusCode)
	}
	if !strings.Contains(string(ri.ResponseBody), "server_error") {
		t.Errorf("应返回 server_error 错误体：%s", ri.ResponseBody)
	}
}

func TestABINotRegisteredFailOpenPassesThrough(t *testing.T) {
	resetABIState(t)
	// fail_open 由寄存的旧配置决定；未寄存时无配置可依，仍按 fail_closed 处理。
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptAfter, mustABIRequest(t, "req-1", `{"content":"hi"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if !ri.Terminate {
		t.Error("无寄存配置时未注册应终止")
	}
}

func TestABIRegisteredFailOpenUnavailablePassesThrough(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("fail_closed: false")})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw); err != nil {
		t.Fatalf("register: %v", err)
	}
	abiState.Lock()
	abiState.plugin = nil
	abiState.Unlock()
	body := `{"content":"hi"}`
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptAfter, mustABIRequest(t, "req-1", body))
	if err != nil {
		t.Fatalf("fail_open 下不可用应放行而非 error：%v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if ri.Terminate {
		t.Error("fail_open 下不可用不应终止")
	}
	// 空 Body 即直通：宿主对 len(resp.Body)==0 保留原请求体（abi-contract §8）。
	if len(ri.Body) != 0 {
		t.Errorf("fail_open 下应空 Body 直通，实际：%s", ri.Body)
	}
}

func TestABIDecodeFailureFailClosedTerminates(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte(`fail_closed: true`)})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw); err != nil {
		t.Fatalf("register: %v", err)
	}
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptAfter, []byte(`{"broken`))
	if err != nil {
		t.Fatalf("fail_closed 下解码失败应返回终止信封而非 error：%v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if !ri.Terminate || ri.StatusCode != http.StatusBadRequest {
		t.Errorf("解码失败应 400 终止，实际 terminate=%v status=%d", ri.Terminate, ri.StatusCode)
	}
	if !strings.Contains(string(ri.ResponseBody), "invalid_request_error") {
		t.Errorf("应返回 invalid_request_error 错误体：%s", ri.ResponseBody)
	}
}

func TestABIDecodeFailureFailOpenPassesThrough(t *testing.T) {
	resetABIState(t)
	cfgRaw, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("fail_closed: false")})
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw); err != nil {
		t.Fatalf("register: %v", err)
	}
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptAfter, []byte(`{"broken`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if ri.Terminate {
		t.Error("fail_open 下解码失败不应终止")
	}
	if ri.Body != nil {
		t.Errorf("解码失败时无正文可放行，实际：%s", ri.Body)
	}
}

func TestABIRecoverConvertsPanicToTerminate(t *testing.T) {
	resetABIState(t)
	cfgReq := abiLifecycleRequest{ConfigYAML: []byte("fail_closed: true\nreject_keywords:\n  - __panic_hook__")}
	cfgRaw, _ := json.Marshal(cfgReq)
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, cfgRaw); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, _ := json.Marshal(abiRequestInterceptRequest{
		RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID:    "req-panic",
			SourceFormat: "openai",
			Body:         []byte(`{"content":"__panic_hook__"}`),
		},
	})
	resp, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, raw)
	if err != nil {
		t.Fatalf("panic 应被 recover 为终止信封而非向上抛：%v", err)
	}
	ri := decodeTerminatedRequest(t, resp)
	if !ri.Terminate || ri.StatusCode != http.StatusInternalServerError {
		t.Errorf("panic 应转为 500 终止，实际 terminate=%v status=%d", ri.Terminate, ri.StatusCode)
	}
	if !strings.Contains(string(ri.ResponseBody), "server_error") {
		t.Errorf("应返回 server_error 错误体：%s", ri.ResponseBody)
	}
}
