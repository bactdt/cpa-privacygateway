package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func newPayloadFixture(t *testing.T, raws ...string) (*StreamRestorer, []string) {
	t.Helper()
	m := newTestMapping(t, 0, 0)
	phs := make([]string, 0, len(raws))
	for _, raw := range raws {
		ph, err := m.Placeholder(raw)
		if err != nil {
			t.Fatalf("Placeholder: %v", err)
		}
		phs = append(phs, ph)
	}
	return NewStreamRestorer(m), phs
}

func jsonText(s string) string {
	return string(encodeTextLiteral(s))
}

// effectiveOut 模拟宿主：changed=false 时沿用原块。
func effectiveOut(t *testing.T, s *StreamRestorer, format, chunk string) string {
	t.Helper()
	out, changed := s.RestorePayload(format, []byte(chunk))
	if !changed && string(out) != chunk {
		t.Fatalf("changed=false but output differs: %q vs %q", out, chunk)
	}
	return string(out)
}

// sseDataPayloads 返回 SSE 文本中全部 data: 载荷，并断言其中的 JSON 合法。
func sseDataPayloads(t *testing.T, text string) []string {
	t.Helper()
	var payloads []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		rest, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(rest, " ")
		if rest != "[DONE]" && !json.Valid([]byte(rest)) {
			t.Fatalf("invalid data payload %q in %q", rest, text)
		}
		payloads = append(payloads, rest)
	}
	return payloads
}

func decodeField(t *testing.T, payload string, fn func(map[string]any) string) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}
	return fn(v)
}

func openAIContent(v map[string]any) string {
	choices, _ := v["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	s, _ := delta["content"].(string)
	return s
}

func TestRestorePayloadShapeDispatch(t *testing.T) {
	cases := []struct {
		chunk string
		want  payloadShape
	}{
		{` {"a":1}`, shapeBare},
		{`[1,2]`, shapeBare},
		{`{"a":`, shapeOpaque},
		{"data: {}\n\n", shapeSSE},
		{"event: x", shapeSSE},
		{"id: 1", shapeSSE},
		{"retry: 10", shapeSSE},
		{": keep-alive\n\n", shapeSSE},
		{"[DONE]", shapeOpaque},
		{"", shapeOpaque},
	}
	for _, tc := range cases {
		if got := payloadShapeOf([]byte(tc.chunk)); got != tc.want {
			t.Errorf("payloadShapeOf(%q) = %d, want %d", tc.chunk, got, tc.want)
		}
	}

	s, phs := newPayloadFixture(t, "user@example.com")
	bare := `{"choices":[{"index":0,"delta":{"content":` + jsonText("a "+phs[0]) + `}}]}`
	if out := effectiveOut(t, s, "openai", bare); !strings.Contains(out, "a user@example.com") {
		t.Fatalf("bare not restored: %s", out)
	}
	frame := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + jsonText(phs[0]) + "}}\n\n"
	if out := effectiveOut(t, s, "claude", frame); !strings.Contains(out, `"text":"user@example.com"`) {
		t.Fatalf("sse not restored: %s", out)
	}
	if out, changed := s.RestorePayload("openai", []byte("[DONE]")); changed || string(out) != "[DONE]" {
		t.Fatalf("opaque changed: %q %v", out, changed)
	}
}

func TestRestorePayloadOpenAIBareSplit(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	ph := phs[0]
	pieces := []string{"邮箱 " + ph[:6], ph[6:20], ph[20:] + " 结束["}
	var got strings.Builder
	for i, piece := range pieces {
		chunk := `{"id":"x","choices":[{"index":0,"delta":{"content":` + jsonText(piece) + `},"finish_reason":null}]}`
		out := effectiveOut(t, s, "openai", chunk)
		if !json.Valid([]byte(out)) {
			t.Fatalf("chunk %d invalid JSON: %s", i, out)
		}
		got.WriteString(decodeField(t, out, openAIContent))
	}
	if got.String() != "邮箱 user@example.com 结束" {
		t.Fatalf("before finish got %q", got.String())
	}
	finish := `{"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	out := effectiveOut(t, s, "openai", finish)
	want := `{"id":"x","choices":[{"index":0,"delta":{"content":"["},"finish_reason":"stop"}]}`
	if out != want {
		t.Fatalf("finish = %s, want %s", out, want)
	}
	st := s.Stats()
	if st.Chunks != 4 || st.Events != 4 || st.Restored != 1 || st.Flushed != 1 || st.Synthesized != 1 || s.Pending() != 0 {
		t.Fatalf("stats = %+v pending=%d", st, s.Pending())
	}
}

func TestRestorePayloadGeminiBareSplit(t *testing.T) {
	s, phs := newPayloadFixture(t, "13812345678")
	ph := phs[0]
	pieces := []string{"手机 " + ph[:10], ph[10:] + " 完 [[PG"}
	var got strings.Builder
	for _, piece := range pieces {
		chunk := `{"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonText(piece) + `}]},"index":0}]}`
		out := effectiveOut(t, s, "gemini", chunk)
		var v struct {
			Candidates []struct {
				Content struct {
					Parts []struct{ Text string }
				}
			}
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("invalid JSON %s: %v", out, err)
		}
		got.WriteString(v.Candidates[0].Content.Parts[0].Text)
	}
	if got.String() != "手机 13812345678 完 " {
		t.Fatalf("got %q", got.String())
	}
	out := effectiveOut(t, s, "gemini", `{"candidates":[{"finishReason":"STOP","index":0}]}`)
	want := `{"candidates":[{"content":{"role":"model","parts":[{"text":"[[PG"}]},"finishReason":"STOP","index":0}]}`
	if out != want {
		t.Fatalf("finish = %s, want %s", out, want)
	}
}

func claudeDelta(text string) string {
	return "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + jsonText(text) + "}}\n\n"
}

func TestRestorePayloadClaudeFrames(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	ph := phs[0]
	tail := ph[:9] // 以本请求占位符前缀结尾，需在 stop 前合成帧投递
	var got strings.Builder
	for _, piece := range []string{"邮箱 " + ph[:12], ph[12:] + " 结束 " + tail} {
		out := effectiveOut(t, s, "claude", claudeDelta(piece))
		for _, p := range sseDataPayloads(t, out) {
			got.WriteString(decodeField(t, p, func(v map[string]any) string {
				return v["delta"].(map[string]any)["text"].(string)
			}))
		}
	}
	if got.String() != "邮箱 user@example.com 结束 " {
		t.Fatalf("got %q", got.String())
	}
	stop := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	out := effectiveOut(t, s, "claude", stop)
	want := claudeDelta(tail) + stop
	if out != want {
		t.Fatalf("stop output:\n%q\nwant\n%q", out, want)
	}
	sseDataPayloads(t, out)
	if st := s.Stats(); st.Synthesized != 1 || st.Flushed != 1 || st.Leftover != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRestorePayloadCodexSingleLines(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	ph := phs[0]
	delta := func(piece string) string {
		return `data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":` + jsonText(piece) + `}`
	}
	var got strings.Builder
	for _, piece := range []string{"邮箱 " + ph[:7], ph[7:] + " 结束 [[PG_"} {
		if out := effectiveOut(t, s, "openai-response", "event: response.output_text.delta"); out != "event: response.output_text.delta" {
			t.Fatalf("event line changed: %q", out)
		}
		out := effectiveOut(t, s, "openai-response", delta(piece))
		if strings.Contains(out, "\n") {
			t.Fatalf("single line gained newline: %q", out)
		}
		for _, p := range sseDataPayloads(t, out) {
			got.WriteString(decodeField(t, p, func(v map[string]any) string { return v["delta"].(string) }))
		}
	}
	if got.String() != "邮箱 user@example.com 结束 " {
		t.Fatalf("got %q", got.String())
	}
	out := effectiveOut(t, s, "openai-response", "event: response.output_text.done")
	want := "event: response.output_text.delta\n" + delta("[[PG_") + "\n\nevent: response.output_text.done"
	if out != want {
		t.Fatalf("done event:\n%q\nwant\n%q", out, want)
	}
	done := `data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":` + jsonText("x "+ph) + `}`
	if out := effectiveOut(t, s, "openai-response", done); !strings.Contains(out, `"text":"x user@example.com"`) {
		t.Fatalf("done text not restored: %s", out)
	}
	if s.Pending() != 0 {
		t.Fatalf("pending = %d", s.Pending())
	}
}

func TestRestorePayloadPassthroughIdentical(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	other := "[[PG_ffffffffffffffff_1]]"
	unknown := strings.Replace(phs[0], "_1]]", "_999]]", 1)
	chunks := []string{
		"[DONE]",
		"data: [DONE]\n\n",
		": keep-alive\n\n",
		"data: {bad\n\n",
		"id: 7\nretry: 100\n\n",
		claudeDelta("plain text, no placeholder"),
		"data:{\"type\":\"ping\"}\r\n\r\n",
		`{"choices":[{"index":0,"delta":{"content":` + jsonText("other "+other+" unk "+unknown) + `}}]}`,
	}
	formats := []string{"openai", "claude", "openai-response", "gemini", ""}
	for _, format := range formats {
		for _, chunk := range chunks {
			in := []byte(chunk)
			out, changed := s.RestorePayload(format, in)
			if changed || len(out) != len(in) || &out[0] != &in[0] {
				t.Fatalf("%s: %q changed=%v out=%q", format, chunk, changed, out)
			}
		}
	}
	if st := s.Stats(); st.Passthrough == 0 || st.Unknown == 0 || st.Restored != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestRestorePayloadEmptyMappingSameSlice(t *testing.T) {
	s := NewStreamRestorer(newTestMapping(t, 0, 0))
	in := []byte(`{"choices":[{"index":0,"delta":{"content":"[[PG_"}}]}`)
	out, changed := s.RestorePayload("openai", in)
	if changed || &out[0] != &in[0] || len(out) != len(in) {
		t.Fatalf("changed=%v out=%q", changed, out)
	}
	nilRestorer := NewStreamRestorer(nil)
	if out, changed := nilRestorer.RestorePayload("openai", in); changed || &out[0] != &in[0] {
		t.Fatalf("nil mapping changed")
	}
	if st := s.Stats(); st.Chunks != 0 {
		t.Fatalf("empty mapping counted: %+v", st)
	}
}

func TestRestorePayloadLineEndingsAndPrefix(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	in := ": c\r\nevent: content_block_delta\r\ndata:{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + jsonText(phs[0]) + "}}\r\n\r\n"
	want := ": c\r\nevent: content_block_delta\r\ndata:{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"user@example.com\"}}\r\n\r\n"
	if out := effectiveOut(t, s, "claude", in); out != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
	// 同一块内多帧：未变化的帧逐字节保留，变化的帧只改载荷。
	two := "data: {\"a\":\"keep <&>\"}\n\ndata: {\"a\":" + jsonText(phs[0]+" <&>") + "}\n\n"
	wantTwo := "data: {\"a\":\"keep <&>\"}\n\ndata: {\"a\":\"user@example.com <&>\"}\n\n"
	if out := effectiveOut(t, s, "openai", two); out != wantTwo {
		t.Fatalf("got %q\nwant %q", out, wantTwo)
	}
}

func TestRestorePayloadCarryOnlyWhenDeliverable(t *testing.T) {
	// openai 的 SSE 形态与 claude 的裸形态不暂扣：前缀残片原样输出。
	s, phs := newPayloadFixture(t, "user@example.com")
	half := phs[0][:10]
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":" + jsonText("a "+half) + "}}]}\n\n"
	if out, changed := s.RestorePayload("openai", []byte(sse)); changed || !bytes.Contains(out, []byte(half)) {
		t.Fatalf("openai sse held back: %q", out)
	}
	bare := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonText(half) + `}}`
	if out, changed := s.RestorePayload("claude", []byte(bare)); changed || string(out) != bare {
		t.Fatalf("claude bare held back: %q", out)
	}
	if s.Pending() != 0 {
		t.Fatalf("pending = %d", s.Pending())
	}
}
