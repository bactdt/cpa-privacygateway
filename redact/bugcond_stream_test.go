package redact

// 流式协议占位符还原的探索测试（bugfix tool-output-placeholder-leak，C7）。
// **Validates: Requirements 2.1–2.7**
//
// 对 (openai,SSE)、(interactions,SSE)、(codex,SSE 帧)、(codex,单行)、
// (antigravity,bare) 五组协议形态，用固定 7 字节切片制造占位符跨块，
// 断言每块一进一出、data: 载荷合法、文本拼接等于 Mapping.Restore(原文)。
// 未修复时代码不识别 interactions/codex/antigravity 的暂存，前缀残片会泄漏。

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixedSlices(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		k := n
		if k > len(s) {
			k = len(s)
		}
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}

func jsonStringField(v any, path ...string) string {
	cur := v
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[seg]
	}
	s, _ := cur.(string)
	return s
}

func openaiSSEExtract(t *testing.T, payload string) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}
	choices, _ := v["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	s, _ := delta["content"].(string)
	return s
}

func interactionsExtract(t *testing.T, payload string) string {
	t.Helper()
	if payload == "[DONE]" {
		return ""
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}
	if v["event_type"] != "step.delta" {
		return ""
	}
	delta, _ := v["delta"].(map[string]any)
	s, _ := delta["text"].(string)
	return s
}

func responsesExtract(t *testing.T, payload string) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}
	if v["type"] != "response.output_text.delta" {
		return ""
	}
	s, _ := v["delta"].(string)
	return s
}

func antigravityExtract(t *testing.T, payload string) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}
	var b strings.Builder
	resp, _ := v["response"].(map[string]any)
	cands, _ := resp["candidates"].([]any)
	for _, raw := range cands {
		c, _ := raw.(map[string]any)
		content, _ := c["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, rp := range parts {
			p, _ := rp.(map[string]any)
			if s, ok := p["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

type bugStreamCase struct {
	name    string
	format  string
	shape   payloadShape
	frame   func(piece string) string
	end     func() string
	extract func(t *testing.T, payload string) string
}

func bugStreamCases() []bugStreamCase {
	return []bugStreamCase{
		{
			name:   "openai-sse",
			format: "openai",
			shape:  shapeSSE,
			frame: func(piece string) string {
				return `data: {"id":"x","choices":[{"index":0,"delta":{"content":` + jsonText(piece) + `},"finish_reason":null}]}` + "\n\n"
			},
			end: func() string {
				return `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
			},
			extract: openaiSSEExtract,
		},
		{
			name:   "interactions-sse",
			format: "interactions",
			shape:  shapeSSE,
			frame: func(piece string) string {
				return "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":" + jsonText(piece) + "}}\n\n"
			},
			end: func() string {
				return "event: done\ndata: [DONE]\n\n"
			},
			extract: interactionsExtract,
		},
		{
			name:   "codex-sse-frames",
			format: "codex",
			shape:  shapeSSE,
			frame: func(piece string) string {
				return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":" + jsonText(piece) + "}\n\n"
			},
			end: func() string {
				return "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"x\"}\n\n"
			},
			extract: responsesExtract,
		},
		{
			name:   "codex-single-lines",
			format: "codex",
			shape:  shapeSSE,
			frame: func(piece string) string {
				return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":" + jsonText(piece) + "}"
			},
			end: func() string {
				return "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"x\"}"
			},
			extract: responsesExtract,
		},
		{
			name:   "antigravity-bare",
			format: "antigravity",
			shape:  shapeBare,
			frame: func(piece string) string {
				return `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":` + jsonText(piece) + `}]}}]}}`
			},
			end: func() string {
				return `{"response":{"candidates":[{"index":0,"finishReason":"STOP"}]}}`
			},
			extract: antigravityExtract,
		},
	}
}

func TestBugCondStreamCarry(t *testing.T) {
	for _, tc := range bugStreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMapping(t, 0, 0)
			ph1, err := m.Placeholder("user@example.com")
			if err != nil {
				t.Fatalf("Placeholder: %v", err)
			}
			ph2, err := m.Placeholder("sk-abcdefghijklmnop1234")
			if err != nil {
				t.Fatalf("Placeholder: %v", err)
			}
			original := "A " + ph1 + " B " + ph2 + " C"
			want := m.Restore(original)

			s := NewStreamRestorer(m)
			var got strings.Builder
			for _, piece := range fixedSlices(original, 7) {
				out, changed := s.RestorePayload(tc.format, []byte(tc.frame(piece)))
				text := string(out)
				if !changed {
					text = tc.frame(piece)
				}
				if tc.shape == shapeBare {
					got.WriteString(tc.extract(t, text))
					continue
				}
				for _, payload := range sseDataPayloads(t, text) {
					got.WriteString(tc.extract(t, payload))
				}
			}
			out, changed := s.RestorePayload(tc.format, []byte(tc.end()))
			text := string(out)
			if !changed {
				text = tc.end()
			}
			if tc.shape == shapeBare {
				got.WriteString(tc.extract(t, text))
			} else {
				for _, payload := range sseDataPayloads(t, text) {
					got.WriteString(tc.extract(t, payload))
				}
			}

			if got.String() != want {
				t.Fatalf("文本流拼接泄漏占位符（C7 复现）：\n got %q\nwant %q\npending=%d", got.String(), want, s.Pending())
			}
			if s.Pending() != 0 {
				t.Fatalf("结束后仍有暂扣：%d", s.Pending())
			}
			if st := s.Stats(); st.Leftover != 0 {
				t.Fatalf("结束后仍有丢弃残留：%+v", st)
			}
		})
	}
}
