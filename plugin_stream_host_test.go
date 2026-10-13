package main

// 宿主形态流块的缺陷条件探索测试（stream-restore-bare-chunks，Property 1）。
// 宿主按行/按事件拆块后交给插件：OpenAI 为裸 JSON、Codex 为单行、Claude 为完整帧；
// 断言插件不丢块、输出合法、拼接后的文本等于原文。
// **Validates: Requirements 1.1, 1.2, 1.3, 1.5, 2.1, 2.2, 2.3, 2.5**

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	hostEmail = "user@example.com"
	hostPhone = "13812345678"
)

// redactedText 经 InterceptRequestBeforeAuth 建立映射，返回占位符化后的文本。
func redactedText(t *testing.T, p *plugin, reqID, text string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"role": "user", "content": text}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		RequestID: reqID, SourceFormat: "openai", Body: body,
	})
	if err != nil {
		t.Fatalf("InterceptRequestBeforeAuth: %v", err)
	}
	if resp.Terminate {
		t.Fatalf("请求不应被终止")
	}
	t.Cleanup(func() {
		_ = p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{RequestID: reqID})
	})
	out := resp.Body
	if len(out) == 0 {
		out = body
	}
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Messages) != 1 {
		t.Fatalf("解析脱敏正文失败: %v %s", err, out)
	}
	return parsed.Messages[0].Content
}

// splitRunes 按给定长度序列（循环使用）以 rune 为单位切分文本。
func splitRunes(text string, sizes []int) []string {
	runes := []rune(text)
	var pieces []string
	for i, k := 0, 0; i < len(runes); k++ {
		n := sizes[k%len(sizes)]
		if n < 1 {
			n = 1
		}
		end := i + n
		if end > len(runes) {
			end = len(runes)
		}
		pieces = append(pieces, string(runes[i:end]))
		i = end
	}
	return pieces
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// openAIChunks 生成宿主形态的 OpenAI 裸 JSON 块（无 data: 前缀、无换行）。
func openAIChunks(text string, sizes []int) [][]byte {
	var chunks [][]byte
	for _, piece := range splitRunes(text, sizes) {
		chunks = append(chunks, []byte(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":`+
			jsonString(piece)+`},"finish_reason":null}]}`))
	}
	chunks = append(chunks, []byte(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	return chunks
}

// effective 返回宿主实际转发的内容：有 Body 取 Body，否则原样转发输入；DropChunk 时为空。
func effective(resp pluginapi.StreamChunkInterceptResponse, in []byte) []byte {
	if resp.DropChunk {
		return nil
	}
	if len(resp.Body) > 0 {
		return resp.Body
	}
	return in
}

// feed 依次投递块，返回每块的有效输出；任何块被丢弃即报错。
func feed(t *testing.T, p *plugin, reqID, format string, chunks [][]byte) [][]byte {
	t.Helper()
	outs := make([][]byte, 0, len(chunks))
	for i, c := range chunks {
		resp, err := p.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: format, ChunkIndex: i, Body: c,
		})
		if err != nil {
			t.Fatalf("chunk %d err: %v", i, err)
		}
		if resp.DropChunk {
			t.Errorf("chunk %d 被丢弃 (DropChunk=true, Body=%q)，输入 %q", i, resp.Body, c)
		}
		outs = append(outs, effective(resp, c))
	}
	return outs
}

// checkOpenAI 断言 OpenAI 裸 JSON 流的输出合法且内容还原为原文。
func checkOpenAI(t *testing.T, outs [][]byte, nChunks int, original string) {
	t.Helper()
	if len(outs) != nChunks {
		t.Errorf("块数变化：%d -> %d", nChunks, len(outs))
	}
	var sb strings.Builder
	for i, out := range outs {
		if !json.Valid(out) {
			t.Errorf("chunk %d 输出不是合法 JSON：%q", i, out)
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(out, &c)
		if len(c.Choices) > 0 {
			sb.WriteString(c.Choices[0].Delta.Content)
		}
	}
	if got := sb.String(); got != original {
		t.Errorf("delta.content 拼接不等于原文：\n got  %q\n want %q", got, original)
	}
	if len(outs) > 0 && !strings.Contains(string(outs[len(outs)-1]), `"finish_reason":"stop"`) {
		t.Errorf("最后一块缺少 finish_reason=stop：%q", outs[len(outs)-1])
	}
}

func TestStreamHostOpenAIBareChunks(t *testing.T) {
	p := newPlugin(defaultConfig())
	original := "邮箱 " + hostEmail + " 手机 " + hostPhone + " 结束"
	redacted := redactedText(t, p, "req-host-openai", original)
	if !strings.Contains(redacted, "[[PG_") {
		t.Fatalf("前置条件：脱敏文本应含占位符：%q", redacted)
	}
	chunks := openAIChunks(redacted, []int{7})
	outs := feed(t, p, "req-host-openai", "openai", chunks)
	checkOpenAI(t, outs, len(chunks), original)
}

func TestStreamHostOpenAIRandomSplitProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	parts := []string{hostEmail, hostPhone, "[", "文本", "ab", "[x]"}
	failures := 0
	for iter := 0; iter < 200 && failures < 3; iter++ {
		n := 2 + rng.Intn(6)
		words := []string{parts[rng.Intn(2)]} // 至少一个敏感值，保证映射非空
		for i := 0; i < n; i++ {
			words = append(words, parts[rng.Intn(len(parts))])
		}
		rng.Shuffle(len(words), func(i, j int) { words[i], words[j] = words[j], words[i] })
		original := strings.Join(words, " ")
		sizes := make([]int, 1+rng.Intn(8))
		for i := range sizes {
			sizes[i] = 1 + rng.Intn(12)
		}
		p := newPlugin(defaultConfig())
		reqID := "req-prop"
		redacted := redactedText(t, p, reqID, original)
		chunks := openAIChunks(redacted, sizes)
		ok := t.Run("", func(t *testing.T) {
			t.Logf("iter=%d original=%q sizes=%v", iter, original, sizes)
			outs := feed(t, p, reqID, "openai", chunks)
			checkOpenAI(t, outs, len(chunks), original)
		})
		if !ok {
			failures++
		}
	}
}

func TestStreamHostCodexSingleLines(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-host-codex"
	original := "邮箱 " + hostEmail + " 手机 " + hostPhone + " 结束"
	redacted := redactedText(t, p, reqID, original)
	var chunks [][]byte
	for _, piece := range splitRunes(redacted, []int{7}) {
		chunks = append(chunks,
			[]byte("event: response.output_text.delta"),
			[]byte(`data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":`+jsonString(piece)+`}`))
	}
	chunks = append(chunks,
		[]byte("event: response.output_text.done"),
		[]byte(`data: {"type":"response.output_text.done","item_id":"m1","output_index":0,"content_index":0,"text":`+jsonString(redacted)+`}`))
	outs := feed(t, p, reqID, "openai-response", chunks)
	var sb strings.Builder
	for i, out := range outs {
		for _, line := range strings.Split(string(out), "\n") {
			payload, isData := strings.CutPrefix(strings.TrimRight(line, "\r"), "data: ")
			if !isData {
				continue
			}
			if !json.Valid([]byte(payload)) {
				t.Errorf("chunk %d data 载荷不是合法 JSON：%q", i, payload)
				continue
			}
			var ev struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal([]byte(payload), &ev)
			if ev.Type == "response.output_text.delta" {
				sb.WriteString(ev.Delta)
			}
		}
	}
	if got := sb.String(); got != original {
		t.Errorf("delta 拼接不等于原文：\n got  %q\n want %q", got, original)
	}
}

func TestStreamHostClaudeSplitAcrossEvents(t *testing.T) {
	p := newPlugin(defaultConfig())
	reqID := "req-host-claude"
	original := "邮箱 " + hostEmail + " 手机 " + hostPhone + " 结束"
	redacted := redactedText(t, p, reqID, original)
	cut := strings.Index(redacted, "[[PG_")
	if cut < 0 {
		t.Fatalf("前置条件：脱敏文本应含占位符：%q", redacted)
	}
	cut += len("[[PG_") + 2 // 切在占位符内部
	frame := func(piece string) []byte {
		return []byte("event: content_block_delta\ndata: " +
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonString(piece) + "}}\n\n")
	}
	chunks := [][]byte{
		frame(redacted[:cut]),
		frame(redacted[cut:]),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
	}
	outs := feed(t, p, reqID, "claude", chunks)
	var sb strings.Builder
	for i, out := range outs {
		for _, line := range strings.Split(string(out), "\n") {
			payload, isData := strings.CutPrefix(line, "data: ")
			if !isData {
				continue
			}
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				t.Errorf("chunk %d data 载荷不是合法 JSON：%q", i, payload)
				continue
			}
			if ev.Type == "content_block_delta" {
				sb.WriteString(ev.Delta.Text)
			}
		}
	}
	if got := sb.String(); got != original {
		t.Errorf("text 拼接不等于原文：\n got  %q\n want %q", got, original)
	}
}
