package main

// 流式还原保留性测试（stream-restore-bare-chunks，Property 2）。
// 在未修复代码上观察并固化非缺陷输入的行为，修复后必须保持不变。
// 只经公共入口 InterceptStreamChunk / InterceptResponse 驱动。
// **Validates: Requirements 3.1, 3.2, 3.3, 3.4, 3.6, 3.7, 3.9**

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"duanjuapp/cpa/redact"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// streamChunk 调用 InterceptStreamChunk 并在出错时终止测试。
func streamChunk(t *testing.T, p *plugin, reqID string, idx int, body []byte) pluginapi.StreamChunkInterceptResponse {
	t.Helper()
	resp, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, SourceFormat: "openai", ChunkIndex: idx, Body: body,
	})
	if err != nil {
		t.Fatalf("chunk %d err: %v", idx, err)
	}
	return resp
}

// placeholderOf 取得原值在本请求映射中的完整占位符。
func placeholderOf(t *testing.T, p *plugin, reqID, value string) string {
	t.Helper()
	m := p.states.mapping(reqID)
	if m == nil || m.Size() == 0 {
		t.Fatalf("前置条件：请求 %s 映射应非空", reqID)
	}
	before := m.Size()
	token, err := m.Placeholder(value) // 已存在时返回既有占位符，不新增条目
	if err != nil || m.Size() != before {
		t.Fatalf("前置条件：%q 应已在映射中：err=%v size %d->%d", value, err, before, m.Size())
	}
	return token
}

func TestStreamPreserveEmptyMapping(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-preserve-empty"
	text := redactedText(t, p, reqID, "你好 今天天气不错 [x] 结束")
	if strings.Contains(text, "[[PG_") {
		t.Fatalf("前置条件：不应产生占位符：%q", text)
	}
	if m := p.states.mapping(reqID); m != nil && m.Size() != 0 {
		t.Fatalf("前置条件：映射应为空，实际 %d", m.Size())
	}
	rng := rand.New(rand.NewSource(1))
	pieces := []string{"ab", "[[PG_", "文本", "[", " x "}
	for i := 0; i < 200; i++ {
		content := jsonString(pieces[rng.Intn(len(pieces))])
		event := `{"choices":[{"index":0,"delta":{"content":` + content + `},"finish_reason":null}]}`
		var chunk []byte
		switch rng.Intn(4) {
		case 0:
			chunk = []byte(event)
		case 1:
			chunk = []byte("data: " + event + "\n\n")
		case 2:
			chunk = []byte("data: [DONE]\n\n")
		default:
			chunk = []byte(": keep-alive\n\n")
		}
		resp := streamChunk(t, p, reqID, i, chunk)
		if len(resp.Body) != 0 || resp.DropChunk {
			t.Fatalf("chunk %d 映射为空时应原样放行：Body=%q Drop=%v 输入 %q", i, resp.Body, resp.DropChunk, chunk)
		}
	}
}

func TestStreamPreserveSwitches(t *testing.T) {
	original := "邮箱 " + hostEmail + " 结束"
	t.Run("disabled", func(t *testing.T) {
		p := newPlugin(defaultConfig())
		reqID := "req-preserve-disabled"
		redacted := redactedText(t, p, reqID, original)
		cfg := defaultConfig()
		cfg.Enabled = false
		p.reconfigure(cfg)
		chunk := []byte("data: " + `{"choices":[{"index":0,"delta":{"content":` + jsonString(redacted) + `}}]}` + "\n\n")
		resp := streamChunk(t, p, reqID, 0, chunk)
		if !reflect.DeepEqual(resp, pluginapi.StreamChunkInterceptResponse{}) {
			t.Errorf("禁用时应返回零值：%+v", resp)
		}
	})
	t.Run("header-init", func(t *testing.T) {
		p := newPlugin(defaultConfig())
		reqID := "req-preserve-header"
		redacted := redactedText(t, p, reqID, original)
		chunk := []byte(`{"choices":[{"index":0,"delta":{"content":` + jsonString(redacted) + `}}]}`)
		resp := streamChunk(t, p, reqID, pluginapi.StreamChunkHeaderInitIndex, chunk)
		if !reflect.DeepEqual(resp, pluginapi.StreamChunkInterceptResponse{}) {
			t.Errorf("header-init 应返回零值：%+v", resp)
		}
	})
	t.Run("empty-body", func(t *testing.T) {
		p := newPlugin(defaultConfig())
		reqID := "req-preserve-emptybody"
		_ = redactedText(t, p, reqID, original)
		resp := streamChunk(t, p, reqID, 0, nil)
		if !reflect.DeepEqual(resp, pluginapi.StreamChunkInterceptResponse{}) {
			t.Errorf("空 Body 应返回零值：%+v", resp)
		}
	})
}

func TestStreamPreserveCompleteFrame(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-preserve-frame"
	_ = redactedText(t, p, reqID, "邮箱 "+hostEmail+" 结束")
	token := placeholderOf(t, p, reqID, hostEmail)
	chunk := []byte(": keep-alive\ndata: " +
		`{"choices":[{"index":0,"delta":{"content":` + jsonString("key: "+token) + `}}]}` + "\n\n")
	resp := streamChunk(t, p, reqID, 0, chunk)
	if resp.DropChunk {
		t.Fatalf("完整帧不应被丢弃")
	}
	out := string(effective(resp, chunk))
	if !strings.Contains(out, hostEmail) || strings.Contains(out, "[[PG_") {
		t.Errorf("应还原为原值：%q", out)
	}
	if !strings.HasPrefix(out, ": keep-alive\n") {
		t.Errorf("注释行应原样保留：%q", out)
	}
	var found bool
	for _, line := range strings.Split(out, "\n") {
		payload, isData := strings.CutPrefix(line, "data: ")
		if !isData {
			continue
		}
		found = true
		if !json.Valid([]byte(payload)) {
			t.Fatalf("data 载荷不是合法 JSON：%q", payload)
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil || len(ev.Choices) != 1 {
			t.Fatalf("解析 data 载荷失败：%v %q", err, payload)
		}
		if got := ev.Choices[0].Delta.Content; got != "key: "+hostEmail {
			t.Errorf("content = %q, want %q", got, "key: "+hostEmail)
		}
	}
	if !found {
		t.Errorf("输出缺少 data: 行：%q", out)
	}
}

func TestStreamPreserveDoneFrame(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-preserve-done"
	_ = redactedText(t, p, reqID, "邮箱 "+hostEmail+" 结束")
	_ = placeholderOf(t, p, reqID, hostEmail)
	chunk := []byte("data: [DONE]\n\n")
	resp := streamChunk(t, p, reqID, 0, chunk)
	if resp.DropChunk {
		t.Fatalf("[DONE] 帧不应被丢弃")
	}
	// 基线缺陷已修复：旧 appendSSELine 对未改动的 data: 行丢失 "data: " 前缀（输出
	// "[DONE]\n\n"）；修复后未改动的行原样回写，[DONE] 帧须逐字节不变（需求 3.4/3.6）。
	out := effective(resp, chunk)
	if !bytes.Equal(out, chunk) {
		t.Errorf("[DONE] 帧应逐字节透传：got %q, want %q", out, chunk)
	}
}

func TestStreamPreserveNonStreamResponse(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-preserve-nonstream"
	_ = redactedText(t, p, reqID, "邮箱 "+hostEmail+" 手机 "+hostPhone)
	email := placeholderOf(t, p, reqID, hostEmail)
	phone := placeholderOf(t, p, reqID, hostPhone)
	body := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":` +
		jsonString("mail "+email+" tel "+phone) + `}}],"meta":{"note":` + jsonString(phone) + `}}`)
	want, count, err := redact.RestoreJSON(append([]byte(nil), body...), p.states.mapping(reqID))
	if err != nil || count != 2 { // count 按被改写的字符串字段计数（content、note）
		t.Fatalf("RestoreJSON 基线：count=%d err=%v", count, err)
	}
	resp, err := p.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		RequestID: reqID, Body: body,
	})
	if err != nil {
		t.Fatalf("InterceptResponse: %v", err)
	}
	if !bytes.Equal(resp.Body, want) {
		t.Errorf("非流式还原应与 RestoreJSON 一致：\n got  %s\n want %s", resp.Body, want)
	}
	if strings.Contains(string(resp.Body), "[[PG_") {
		t.Errorf("不应残留占位符：%s", resp.Body)
	}
}
