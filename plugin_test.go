package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"duanjuapp/cpa/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNewPlugin(t *testing.T) {
	cfg := defaultConfig()
	p := newPlugin(cfg)
	if p == nil {
		t.Fatal("newPlugin 不应返回 nil")
	}
	if p.states == nil {
		t.Error("states 应初始化")
	}
}

func TestPluginReconfigure(t *testing.T) {
	cfg := defaultConfig()
	p := newPlugin(cfg)
	newCfg := defaultConfig()
	newCfg.MaxMappings = 200
	p.reconfigure(newCfg)
	if p.cfg.MaxMappings != 200 {
		t.Errorf("reconfigure 后 MaxMappings = %d", p.cfg.MaxMappings)
	}
}

func TestPluginInterceptRequestBeforeAuthEmptyID(t *testing.T) {
	p := newPlugin(defaultConfig())
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Body:         []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("空 RequestID 在 fail-closed 下应终止请求")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
	if p.states.activeCount() != 0 {
		t.Error("空 RequestID 不应占用准入计数")
	}
}

func TestPluginInterceptRequestBeforeAuthEmptyIDFailOpen(t *testing.T) {
	cfg := defaultConfig()
	closed := false
	cfg.FailClosed = &closed
	p := newPlugin(cfg)
	body := []byte(`{"content":"sk-abcdefghijklmnop1234"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("fail-open 下空 RequestID 不应终止请求")
	}
	if !strings.Contains(string(resp.Body), "[") || string(resp.Body) == string(body) {
		t.Errorf("fail-open 下空 RequestID 应做无状态脱敏：%s", resp.Body)
	}
	if p.states.activeCount() != 0 {
		t.Error("无状态脱敏不应写入状态表")
	}
}

func TestPluginInterceptRequestAfterAuthEmptyID(t *testing.T) {
	p := newPlugin(defaultConfig())
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Body:         []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("AfterAuth 空 RequestID 在 fail-closed 下应终止请求")
	}
}

func TestPluginInterceptRequestDuplicateIDRedacts(t *testing.T) {
	p := newPlugin(defaultConfig())
	first := []byte(`{"content":"sk-abcdefghijklmnop1234"}`)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: first,
	}); err != nil {
		t.Fatalf("err: %v", err)
	}
	second := []byte(`{"content":"sk-abcdefghijklmnop5678"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: second,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("重复 RequestID 不应终止请求")
	}
	if string(resp.Body) == string(second) {
		t.Error("重复 RequestID 不应原样放行未脱敏正文")
	}
	if strings.Contains(string(resp.Body), "sk-abcdefghijklmnop5678") {
		t.Errorf("重复 RequestID 的新敏感值应被脱敏：%s", resp.Body)
	}
	if p.states.activeCount() != 1 {
		t.Errorf("重复 RequestID 不应重复计数，active = %d", p.states.activeCount())
	}
	restored, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		RequestID: "req-1", Body: resp.Body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(restored.Body), "sk-abcdefghijklmnop5678") {
		t.Errorf("重复 ID 的响应应能还原：%s", restored.Body)
	}
}

func TestPluginInterceptRequestDuplicateIDInconsistentFormat(t *testing.T) {
	p := newPlugin(defaultConfig())
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: []byte(`{"content":"hello"}`),
	}); err != nil {
		t.Fatalf("err: %v", err)
	}
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "claude", Body: []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("重复 RequestID 换格式重用应终止请求")
	}
}

func TestPluginInterceptRequestBeforeAuthUnsupportedFormat(t *testing.T) {
	cfg := defaultConfig()
	cfg.FailClosed = boolPtr(true)
	p := newPlugin(cfg)
	body := []byte(`{"model":"gpt-4"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "unsupported-format",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("fail_closed 下不支持的格式应 terminate")
	}
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
}

func TestPluginInterceptRequestBeforeAuthEmptyFormatPassThrough(t *testing.T) {
	p := newPlugin(defaultConfig())
	body := []byte(`{"model":"gpt-4"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1",
		Body:      body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("空格式在 fail_closed=false 下应放行")
	}
}

func TestPluginInterceptRequestBeforeAuthKeywordBlock(t *testing.T) {
	cfg := defaultConfig()
	cfg.RejectKeywords = []string{"blocked_keyword"}
	p := newPlugin(cfg)
	body := []byte(`{"text":"this has blocked_keyword inside"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("含关键词的请求应 terminate")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
}

func TestPluginInterceptRequestBeforeAuthRateLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxConcurrency = 1
	p := newPlugin(cfg)
	p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"model":"gpt-4"}`),
	})
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-2",
		SourceFormat: "openai",
		Body:         []byte(`{"model":"gpt-4"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("超过并发限制应 terminate")
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
}

func TestPluginInterceptRequestBeforeAuthRedacts(t *testing.T) {
	p := newPlugin(defaultConfig())
	body := []byte(`{"messages":[{"role":"user","content":"key sk-abcdefghijklmnop1234 here"}]}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("正常请求不应 terminate")
	}
	if strings.Contains(string(resp.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("敏感数据应被脱敏：%s", resp.Body)
	}
}

func TestPluginInterceptRequestAfterAuth(t *testing.T) {
	p := newPlugin(defaultConfig())
	body := []byte(`{"text":"hello"}`)
	_, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("已有 mapping 的请求不应 terminate")
	}
}

func TestPluginInterceptResponse(t *testing.T) {
	p := newPlugin(defaultConfig())
	// Setup: request first
	reqBody := []byte(`{"messages":[{"role":"user","content":"my secret is sk-abcdefghijklmnop1234"}]}`)
	_, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         reqBody,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Get the redacted body that was returned
	redactedResp, _ := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         reqBody,
	})
	// Second call with same ID returns the body from first call
	_ = redactedResp

	// Now intercept response with placeholder
	mapping := p.states.mapping("req-1")
	if mapping == nil {
		t.Fatal("mapping 应存在")
	}
	// Build a response body with the placeholder
	token, _ := mapping.Placeholder("sk-abcdefghijklmnop1234")
	respBody := []byte(`{"content":"key is ` + token + ` done"}`)
	resp, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		RequestID: "req-1",
		Body:      respBody,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(string(resp.Body), token) {
		t.Errorf("占位符应被还原：%s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("应还原为原始值：%s", resp.Body)
	}
}

func TestPluginInterceptStreamChunk(t *testing.T) {
	p := newPlugin(defaultConfig())
	const secret = "sk-abcdefghijklmnop1234"
	reqBody := []byte(`{"messages":[{"role":"user","content":"my secret is ` + secret + `"}]}`)
	p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         reqBody,
	})
	mapping := p.states.mapping("req-1")
	if mapping == nil {
		t.Fatal("mapping 应存在")
	}
	token, _ := mapping.Placeholder(secret)
	// 宿主形态：OpenAI 裸 JSON chunk（无 data: 前缀、无换行），占位符跨两块。
	mk := func(content string) []byte {
		b, _ := json.Marshal(map[string]any{
			"object":  "chat.completion.chunk",
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": content}}},
		})
		return b
	}
	chunks := [][]byte{
		mk("key is " + token[:len(token)/2]),
		mk(token[len(token)/2:] + " done"),
	}
	var sb strings.Builder
	for i, c := range chunks {
		resp, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
			RequestID:    "req-1",
			SourceFormat: "openai",
			ChunkIndex:   i,
			Body:         c,
		})
		if err != nil {
			t.Fatalf("chunk %d err: %v", i, err)
		}
		if resp.DropChunk {
			t.Errorf("chunk %d 不应被丢弃", i)
		}
		out := c
		if len(resp.Body) > 0 {
			out = resp.Body
		}
		var parsed struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("chunk %d 输出不是合法 JSON：%q", i, out)
		}
		if len(parsed.Choices) > 0 {
			sb.WriteString(parsed.Choices[0].Delta.Content)
		}
	}
	if got, want := sb.String(), "key is "+secret+" done"; got != want {
		t.Errorf("delta.content 拼接应还原原文：got %q want %q", got, want)
	}
}

func TestPluginHandleRequestComplete(t *testing.T) {
	p := newPlugin(defaultConfig())
	p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"model":"gpt-4"}`),
	})
	if p.states.activeCount() != 1 {
		t.Errorf("activeCount = %d", p.states.activeCount())
	}
	err := p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.states.activeCount() != 0 {
		t.Errorf("完成后 activeCount = %d", p.states.activeCount())
	}
}

func TestPluginSupportedFormats(t *testing.T) {
	expected := []string{"openai", "openai-response", "claude", "gemini", "codex", "antigravity", "interactions"}
	for _, f := range expected {
		if !supportedFormats[f] {
			t.Errorf("格式 %q 应被支持", f)
		}
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestTerminate(t *testing.T) {
	resp, err := terminate(http.StatusForbidden, "test_error", "test message", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("Terminate 应为 true")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
	if resp.ResponseHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %s", resp.ResponseHeaders.Get("Content-Type"))
	}
}

func TestPluginSettings(t *testing.T) {
	cfg := defaultConfig()
	p := newPlugin(cfg)
	c, d := p.settings()
	if c.MaxMappings != cfg.MaxMappings {
		t.Errorf("MaxMappings = %d", c.MaxMappings)
	}
	if !d.Credentials {
		t.Error("detector.Credentials 应为 true")
	}
}

var _ = redact.KindEmail

func TestPluginCapacityExceededFailClosed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxMappings = 1
	closed := true
	cfg.FailClosed = &closed
	p := newPlugin(cfg)
	body := []byte(`{"content":"sk-abcdefghijklmnop1234","input":"sk-abcdefghijklmnop5678"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Terminate {
		t.Error("fail-closed 下映射容量超限应终止请求")
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
	if p.states.activeCount() != 0 {
		t.Error("容量超限后应释放准入计数")
	}
}

func TestPluginCapacityExceededFailOpen(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxMappings = 1
	closed := false
	cfg.FailClosed = &closed
	p := newPlugin(cfg)
	body := []byte(`{"content":"sk-abcdefghijklmnop1234","input":"sk-abcdefghijklmnop5678"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Terminate {
		t.Error("fail-open 下容量超限不应终止请求")
	}
	if string(resp.Body) != string(body) {
		t.Errorf("fail-open 下应原样放行：%s", resp.Body)
	}
	if p.states.mapping("req-1") != nil {
		t.Error("容量超限失败时不应写入映射")
	}
}

func TestPluginReconfigureKeepsInflightMapping(t *testing.T) {
	p := newPlugin(defaultConfig())
	p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	mapping := p.states.mapping("req-1")
	if mapping == nil {
		t.Fatal("mapping 应存在")
	}
	newCfg := defaultConfig()
	newCfg.MaxMappings = 200
	p.reconfigure(newCfg)
	if p.states.mapping("req-1") != mapping {
		t.Error("reconfigure 不应清空在途 mapping")
	}
}

func TestPluginReconfigureTightensLimitKeepsEntries(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxRequests = 8
	p := newPlugin(cfg)
	p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	newCfg := defaultConfig()
	newCfg.MaxRequests = 4
	p.reconfigure(newCfg)
	if p.states.maxItems != 4 {
		t.Errorf("maxItems = %d, want 4", p.states.maxItems)
	}
	if p.states.get("req-1") == nil {
		t.Error("收紧上限不应清掉已在途条目")
	}
}

func TestPluginRegisterHandsOverInflightState(t *testing.T) {
	req, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("max_mappings: 50")})
	register := func() {
		if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, req); err != nil {
			t.Fatalf("注册: %v", err)
		}
	}
	abiState.Lock()
	abiState.plugin = nil
	abiState.stopJanitor = nil
	abiState.shuttingDown = false
	abiState.Unlock()
	defer func() {
		abiState.Lock()
		abiState.plugin = nil
		abiState.stopJanitor = nil
		abiState.Unlock()
	}()
	register()
	first := abiState.plugin
	first.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"sk-abcdefghijklmnop1234"}`),
	})
	mapping := first.states.mapping("req-1")
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, req); err != nil {
		t.Fatalf("再次注册: %v", err)
	}
	second := abiState.plugin
	if second == first {
		t.Fatal("再次注册应替换 plugin 实例")
	}
	if second.states.get("req-1") == nil {
		t.Error("重新注册应移交在途条目")
	}
	if second.states.mapping("req-1") != mapping {
		t.Error("mapping 应随移交保留")
	}
	if first.states.get("req-1") == nil {
		t.Error("旧 plugin 状态表读取应跟随移交后的新实例")
	}
	if first.states.mapping("req-1") != mapping {
		t.Error("旧 plugin 状态表读取 mapping 应跟随移交后的新实例")
	}
}

func TestPluginAfterAuthRedactsNewSecrets(t *testing.T) {
	p := newPlugin(defaultConfig())
	body := []byte(`{"content":"after-auth sk-abcdefghijklmnop1234"}`)
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(string(resp.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("after-auth 新增敏感值必须脱敏：%s", resp.Body)
	}
	mapping := p.states.mapping("req-1")
	if mapping == nil {
		t.Fatal("after-auth 脱敏后应写回状态表以便响应还原")
	}
	restored, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		RequestID: "req-1",
		Body:      resp.Body,
	})
	if err != nil {
		t.Fatalf("InterceptResponse: %v", err)
	}
	if !strings.Contains(string(restored.Body), "sk-abcdefghijklmnop1234") {
		t.Errorf("响应应还原原文：%s", restored.Body)
	}
}

func TestPluginAfterAuthRedactsModifiedBodyWithExistingMapping(t *testing.T) {
	p := newPlugin(defaultConfig())
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"first sk-abcdefghijklmnop1234"}`),
	}); err != nil {
		t.Fatalf("before-auth: %v", err)
	}
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"second sk-1234567890abcdef"}`),
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if strings.Contains(string(resp.Body), "sk-1234567890abcdef") {
		t.Errorf("已有映射时 after-auth 修改正文的新敏感值也必须脱敏：%s", resp.Body)
	}
}

func TestPluginTextArrayNestedObjectNotScanned(t *testing.T) {
	m, errNewMapping := redact.NewMapping(1024, 1<<20)
	if errNewMapping != nil {
		t.Fatalf("NewMapping: %v", errNewMapping)
	}
	body := []byte(`{"input":[{"metadata":"sk-abcdefghijklmnop1234","unknown_field":"sk-1234567890abcdef"}]}`)
	out, count, err := redact.RedactJSON("openai", body, m, redact.DefaultConfig(), redact.WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count != 0 {
		t.Errorf("text 数组内结构对象子树不应被数组上下文误扫，count=%d out=%s", count, out)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("应原样保留：%s", out)
	}
}

func TestPluginTextArrayOfStringsStillScanned(t *testing.T) {
	m, errNewMapping := redact.NewMapping(1024, 1<<20)
	if errNewMapping != nil {
		t.Fatalf("NewMapping: %v", errNewMapping)
	}
	body := []byte(`{"input":["sk-abcdefghijklmnop1234"]}`)
	out, count, err := redact.RedactJSON("openai", body, m, redact.DefaultConfig(), redact.WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count == 0 {
		t.Fatalf("字符串数组直接元素应脱敏")
	}
	if bytes.Contains(out, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("数组内敏感值未脱敏：%s", out)
	}
}

func TestPluginLifecycleShutdownBlocksReregister(t *testing.T) {
	req, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("max_mappings: 50")})
	abiState.Lock()
	abiState.plugin = nil
	abiState.stopJanitor = nil
	abiState.shuttingDown = false
	abiState.Unlock()
	defer func() {
		abiState.Lock()
		abiState.plugin = nil
		abiState.stopJanitor = nil
		abiState.shuttingDown = false
		abiState.Unlock()
	}()
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, req); err != nil {
		t.Fatalf("注册: %v", err)
	}
	PrivacyGatewayPluginShutdown()
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, req); err == nil {
		t.Error("shutdown 后注册应被拒绝，不得复活插件")
	}
	if abiState.plugin != nil {
		t.Error("shutdown 后不得持有插件实例")
	}
}

func TestPluginHotReloadKeepsInflightAcrossReconfigure(t *testing.T) {
	req, _ := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("max_mappings: 50")})
	abiState.Lock()
	abiState.plugin = nil
	abiState.stopJanitor = nil
	abiState.shuttingDown = false
	abiState.Unlock()
	defer func() {
		abiState.Lock()
		abiState.plugin = nil
		abiState.stopJanitor = nil
		abiState.shuttingDown = false
		abiState.Unlock()
	}()
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginRegister, req); err != nil {
		t.Fatalf("注册: %v", err)
	}
	first := abiState.plugin
	if _, err := handleABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, mustABIRequest(t, "req-1", `{"content":"sk-abcdefghijklmnop1234"}`)); err != nil {
		t.Fatalf("请求拦截: %v", err)
	}
	mapping := first.states.mapping("req-1")
	if _, err := handleABIRegister(context.Background(), pluginabi.MethodPluginReconfigure, req); err != nil {
		t.Fatalf("重配: %v", err)
	}
	second := abiState.plugin
	if second == first {
		t.Fatal("重配应替换实例")
	}
	got := second.states.mapping("req-1")
	if got != mapping {
		t.Error("热重载后在途映射应可从新实例访问")
	}
}

func TestPluginGuardAppliesToReusedRequestID(t *testing.T) {
	cfg := defaultConfig()
	cfg.RejectKeywords = []string{"forbidden"}
	cfg.MaxBodyBytes = 64
	p := newPlugin(cfg)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"first sk-abcdefghijklmnop1234"}`),
	}); err != nil {
		t.Fatalf("首个请求应通过: %v", err)
	}
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"with forbidden keyword"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Body != nil && !strings.Contains(string(resp.Body), "permission_error") {
		t.Errorf("复用 ID 的正文命中关键词应被拦截：%s", resp.Body)
	}
	big := make([]byte, cfg.MaxBodyBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	resp, err = p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         big,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Body != nil && !strings.Contains(string(resp.Body), "exceeds privacy gateway limit") {
		t.Errorf("复用 ID 的超限正文应被拦截：%s", resp.Body)
	}
	if p.states.mapping("req-1") == nil {
		t.Error("拦截复用正文不应释放首请求的状态条目")
	}
}

func TestPluginGuardAppliesToAfterAuth(t *testing.T) {
	cfg := defaultConfig()
	cfg.RejectKeywords = []string{"forbidden"}
	p := newPlugin(cfg)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"first sk-abcdefghijklmnop1234"}`),
	}); err != nil {
		t.Fatalf("首个请求应通过: %v", err)
	}
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID:    "req-1",
		SourceFormat: "openai",
		Body:         []byte(`{"content":"after forbidden"}`),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Body != nil && !strings.Contains(string(resp.Body), "permission_error") {
		t.Errorf("after-auth 正文命中关键词应被拦截：%s", resp.Body)
	}
}

func TestPluginDisabledPassthrough(t *testing.T) {
	cfg := defaultConfig()
	cfg.Enabled = false
	cfg.RejectKeywords = []string{"forbidden"}
	p := newPlugin(cfg)
	body := []byte(`{"content":"sk-abcdefghijklmnop1234 forbidden"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("before-auth: %v", err)
	}
	if string(resp.Body) != string(body) {
		t.Errorf("禁用后请求应原样放行：%s", resp.Body)
	}
	resp2, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if string(resp2.Body) != string(body) {
		t.Errorf("禁用后 after-auth 应原样放行：%s", resp2.Body)
	}
	resp3, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		RequestID: "req-1", Body: []byte(`{"content":"[[PG_x_1]]"}`),
	})
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp3.Body != nil {
		t.Errorf("禁用后响应不应改写：%s", resp3.Body)
	}
	resp4, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		RequestID: "req-1", ChunkIndex: 1, Body: []byte("data: hello\n\n"),
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp4.Body != nil {
		t.Errorf("禁用后流不应改写：%s", resp4.Body)
	}
	if p.states.mapping("req-1") != nil {
		t.Error("禁用后不应产生状态条目")
	}
}

func TestPluginMaxFindingsRejectsWhenFailClosed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxFindings = 1
	p := newPlugin(cfg)
	body := []byte(`{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih c sk-mlkjihgfedcbazyxwv"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-1", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("fail_closed 下超限应 500，实际 %d", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(resp.ResponseBody, &payload)
	if payload.Error.Type != "max_findings_exceeded" {
		t.Errorf("应返回 max_findings_exceeded：%s", resp.ResponseBody)
	}
}

func TestPluginMaxFindingsPassesThroughWhenFailOpen(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxFindings = 1
	open := false
	cfg.FailClosed = &open
	p := newPlugin(cfg)
	body := []byte(`{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-open", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.StatusCode != 0 && resp.StatusCode != http.StatusOK {
		t.Fatalf("fail_open 应放行，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "sk-zyxwvutsrqponmlkjih") {
		t.Errorf("fail_open 超限应原样放行：%s", resp.Body)
	}
}

func TestPluginMaxFindingsWithinLimitRedacts(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxFindings = 2
	p := newPlugin(cfg)
	body := []byte(`{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih"}`)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-within", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := strings.Count(string(resp.Body), "[[PG_"); got != 2 {
		t.Errorf("限额内应全部替换（2 处），实际 %d：%s", got, resp.Body)
	}
}

func TestPluginExistingStateMaxFindingsFailClosed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxFindings = 1
	p := newPlugin(cfg)
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-exist", SourceFormat: "openai", Body: []byte(`{"content":"ok text"}`),
	})
	if err != nil {
		t.Fatalf("建立状态失败: %v", err)
	}
	_ = resp
	if p.states.get("req-exist") == nil {
		t.Fatal("状态条目应存在")
	}
	after, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-exist", SourceFormat: "openai", Body: []byte(`{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih c sk-mlkjihgfedcbazyxwv"}`),
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if !after.Terminate || after.StatusCode != http.StatusInternalServerError {
		t.Errorf("已有状态路径超限应 500 终止，实际 terminate=%v status=%d", after.Terminate, after.StatusCode)
	}
	if strings.Contains(string(after.ResponseBody), "sk-abcdefghijklmnop1234") {
		t.Errorf("不得放行原文：%s", after.ResponseBody)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	json.Unmarshal(after.ResponseBody, &payload)
	if payload.Error.Type != "max_findings_exceeded" {
		t.Errorf("应返回 max_findings_exceeded：%s", after.ResponseBody)
	}
	if p.states.get("req-exist") == nil {
		t.Error("拒绝不应释放已有状态条目（首请求可能仍在途）")
	}
}

func TestPluginReusedRequestIDMaxFindingsFailClosed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxFindings = 1
	p := newPlugin(cfg)
	first, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-reuse", SourceFormat: "openai", Body: []byte(`{"content":"ok text"}`),
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_ = first
	retry, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-reuse", SourceFormat: "openai", Body: []byte(`{"content":"a sk-abcdefghijklmnop1234 b sk-zyxwvutsrqponmlkjih"}`),
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !retry.Terminate || retry.StatusCode != http.StatusInternalServerError {
		t.Errorf("同 ID 重试超限应 500 终止，实际 terminate=%v status=%d", retry.Terminate, retry.StatusCode)
	}
	if strings.Contains(string(retry.ResponseBody), "sk-abcdefghijklmnop1234") {
		t.Errorf("不得放行原文：%s", retry.ResponseBody)
	}
	if p.states.get("req-reuse") == nil {
		t.Error("重试拒绝不应释放复用条目")
	}
}

func TestPluginExistingStateMalformedJSONFailClosed(t *testing.T) {
	cfg := defaultConfig()
	p := newPlugin(cfg)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-bad", SourceFormat: "openai", Body: []byte(`{"content":"ok"}`),
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-bad", SourceFormat: "openai", Body: []byte(`{"content":`),
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if !resp.Terminate || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("已有状态 JSON 无效应 400 终止，实际 terminate=%v status=%d", resp.Terminate, resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	json.Unmarshal(resp.ResponseBody, &payload)
	if payload.Error.Type != "invalid_request_error" {
		t.Errorf("应返回 invalid_request_error：%s", resp.ResponseBody)
	}
}

func TestPluginExistingStateMalformedJSONFailOpen(t *testing.T) {
	cfg := defaultConfig()
	open := false
	cfg.FailClosed = &open
	p := newPlugin(cfg)
	if _, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-bad-open", SourceFormat: "openai", Body: []byte(`{"content":"ok"}`),
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	resp, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-bad-open", SourceFormat: "openai", Body: []byte(`{"content":`),
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if resp.Terminate {
		t.Error("fail_open JSON 无效应放行")
	}
	if string(resp.Body) != `{"content":` {
		t.Errorf("fail_open 应原样放行，实际：%s", resp.Body)
	}
}

func TestPluginAfterAuthPlaceholderOnlyPassesThrough(t *testing.T) {
	cfg := defaultConfig()
	p := newPlugin(cfg)
	body := []byte(`{"content":"the key is sk-abcdefghijklmnop1234 here"}`)
	before, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-ph", SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("before-auth: %v", err)
	}
	if before.Terminate || !strings.Contains(string(before.Body), "[[PG_") {
		t.Fatalf("before-auth 应脱敏出占位符：%s", before.Body)
	}
	mappingCount := p.states.mapping("req-ph").Size()
	after, err := p.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: "req-ph", SourceFormat: "openai", Body: before.Body,
	})
	if err != nil {
		t.Fatalf("after-auth: %v", err)
	}
	if after.Terminate {
		t.Error("只含占位符的正文不应终止")
	}
	if string(after.Body) != string(before.Body) {
		t.Errorf("占位符正文应原样放行：\nbefore=%s\nafter=%s", before.Body, after.Body)
	}
	if got := p.states.mapping("req-ph").Size(); got != mappingCount {
		t.Errorf("映射数应不变：before=%d after=%d", mappingCount, got)
	}
}
