package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func FuzzDetect(f *testing.F) {
	f.Add("sk-abcdefghijklmnop1234 tel 13812345678 a@b.com")
	f.Add("ts=1728546241000 order=1231385962410001")
	f.Add("id 11010519491231002X card 4111 1111 1111 1111")
	f.Add("\x00\xff\xfe mixed bytes \u4e2d\u6587")
	f.Add(strings.Repeat("A", 512))
	f.Fuzz(func(t *testing.T, text string) {
		cfg := DefaultConfig()
		spans, _ := cfg.Detect(text, 0, nil)
		for _, s := range spans {
			if s.start < 0 || s.end > len(text) || s.start >= s.end {
				t.Fatalf("非法 span [%d,%d) len=%d", s.start, s.end, len(text))
			}
		}
		resolveSpans(spans)
	})
}

func FuzzWalkJSON(f *testing.F) {
	f.Add([]byte(`{"messages":[{"content":"sk-abcdefghijklmnop1234"}]}`))
	f.Add([]byte(`{"input":["text1","text2"],"meta":{"nested":{"text":"deep"}}}`))
	f.Add([]byte(`{"tool":"fn","parameters":{"query":"x"}}`))
	f.Add([]byte(`{"a":"\u00e4\ud83d\ude00","b":[1,2.5,true,null]}`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, body []byte) {
		m, err := NewMapping(4096, 1<<20)
		if err != nil {
			t.Skip()
		}
		cfg := DefaultConfig()
		for _, format := range []string{"openai", "anthropic", "gemini", ""} {
			out, count, err := RedactJSON(format, body, m, cfg, WalkOptions{Format: format})
			if err != nil && !errors.Is(err, ErrMalformedJSON) {
				t.Fatalf("format=%q 意外错误: %v", format, err)
			}
			if count > 0 && len(out) == 0 {
				t.Fatalf("count>0 但输出为空")
			}
		}
	})
}

func FuzzStreamRestorer(f *testing.F) {
	f.Add([]byte(`{"delta":{"content":"Hello [[PG_abcdef1234567890_1]]"}}`+"\n\n"), uint8(20))
	f.Add([]byte("data: {\"a\":1}\n\ndata: {\"b\":\"[[PG_abcdef1234567890_1]]\"}\n\n"), uint8(11))
	f.Add([]byte("x"), uint8(1))
	f.Fuzz(func(t *testing.T, blob []byte, split uint8) {
		if len(blob) == 0 || len(blob) > 8192 {
			t.Skip()
		}
		cut := int(split) % (len(blob) + 1)
		chunks := []string{string(blob[:cut]), string(blob[cut:])}
		m, err := NewMapping(1024, 1<<20)
		if err != nil {
			t.Skip()
		}
		raw := "sk-abcdefghijklmnop1234"
		ph, err := m.Placeholder(raw)
		if err != nil {
			t.Skip()
		}
		whole := strings.Join(chunks, "")
		if !strings.Contains(whole, ph) {
			t.Skip()
		}
		want := m.Restore(whole)

		r := NewStreamRestorer(m)
		var got strings.Builder
		for _, chunk := range chunks {
			out, drop := r.RestoreChunk([]byte(chunk))
			if drop {
				t.Fatalf("意外丢块")
			}
			got.Write(out)
		}
		got.Write(r.Flush())
		if got.String() != want {
			t.Fatalf("切分还原不一致\nwhole=%q\ngot =%q", want, got.String())
		}
	})
}

// fuzzDataPayloads 按 restoreSSE 的切行规则统计 data: 载荷中合法 / 非法 JSON 的个数。
func fuzzDataPayloads(text []byte) (valid, invalid int) {
	for _, line := range strings.Split(string(text), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), "data:")
		if !ok {
			continue
		}
		if json.Valid([]byte(strings.TrimPrefix(rest, " "))) {
			valid++
		} else {
			invalid++
		}
	}
	return valid, invalid
}

// FuzzRestorePayload：任意输入不 panic；changed=false 时返回原块；原本合法的
// 裸 JSON 输出仍合法；SSE 中原本合法的 data: 载荷仍合法（合成帧只增加合法载荷）。
// 每次先投递一个按协议留下暂扣的预热块，让被测块也覆盖注入 / 合成路径。
func FuzzRestorePayload(f *testing.F) {
	const otherPH = "[[PG_0123456789abcdef_1]]" // 其他 nonce 的占位符，应原样保留
	f.Add(uint8(0), []byte(`{"choices":[{"index":0,"delta":{"content":"a [[PG_"},"finish_reason":"stop"}]}`))
	f.Add(uint8(1), []byte(`{"candidates":[{"finishReason":"STOP","index":0}]}`))
	f.Add(uint8(2), []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
	f.Add(uint8(2), []byte("data: [DONE]\n\n"))
	f.Add(uint8(0), []byte("[DONE]"))
	f.Add(uint8(2), []byte(": keep-alive\n\n"))
	f.Add(uint8(3), []byte("event: response.output_text.done"))
	f.Add(uint8(3), []byte(`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`))
	f.Add(uint8(3), []byte("event: response.completed\r\ndata:{\"type\":\"response.completed\"}\r\n\r\n"))
	f.Add(uint8(4), []byte(`{"a":"`+otherPH+`"}`))
	f.Add(uint8(0), []byte("data: {bad\n\n{\"x\":"))
	f.Fuzz(func(t *testing.T, formatIndex uint8, chunk []byte) {
		if len(chunk) > 16384 {
			t.Skip()
		}
		formats := []string{"openai", "gemini", "claude", "openai-response", ""}
		format := formats[int(formatIndex)%len(formats)]
		m, err := NewMapping(64, 1<<16)
		if err != nil {
			t.Skip()
		}
		real, err := m.Placeholder("user@example.com")
		if err != nil {
			t.Skip()
		}
		half := jsonText("a " + real[:10])
		warm := map[string]string{
			"openai":          `{"choices":[{"index":0,"delta":{"content":` + half + `}}]}`,
			"gemini":          `{"candidates":[{"content":{"parts":[{"text":` + half + `}]},"index":0}]}`,
			"claude":          "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + half + "}}\n\n",
			"openai-response": `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":` + half + `}`,
		}
		r := NewStreamRestorer(m)
		if w, ok := warm[format]; ok {
			r.RestorePayload(format, []byte(w))
		}
		in := append([]byte(nil), chunk...)
		shape := payloadShapeOf(in)
		out, changed := r.RestorePayload(format, in)
		if !changed && !bytes.Equal(out, chunk) {
			t.Fatalf("changed=false but output differs: %q vs %q", out, chunk)
		}
		switch shape {
		case shapeBare:
			if !json.Valid(out) {
				t.Fatalf("bare JSON became invalid:\nin =%q\nout=%q", chunk, out)
			}
		case shapeSSE:
			inValid, inInvalid := fuzzDataPayloads(chunk)
			outValid, outInvalid := fuzzDataPayloads(out)
			if outInvalid != inInvalid || outValid < inValid {
				t.Fatalf("data payload validity changed (%d/%d -> %d/%d):\nin =%q\nout=%q",
					inValid, inInvalid, outValid, outInvalid, chunk, out)
			}
		default:
			if changed {
				t.Fatalf("opaque chunk changed: %q -> %q", chunk, out)
			}
		}
	})
}

func BenchmarkDetect(b *testing.B) {
	cfg := DefaultConfig()
	text := "user 13812345678 email a@example.com card 4111 1111 1111 1111 id 110105194912310012 sk-abcdefghijklmnop1234 ts=1728546241000"
	b.ReportAllocs()
	for b.Loop() {
		_, _ = cfg.Detect(text, 0, nil)
	}
}

func BenchmarkRedactJSON(b *testing.B) {
	m, err := NewMapping(1024, 1<<20)
	if err != nil {
		b.Fatal(err)
	}
	cfg := DefaultConfig()
	body := []byte(`{"messages":[{"role":"user","content":"我的卡号 4111 1111 1111 1111，邮箱 a@example.com，key sk-abcdefghijklmnop1234"},{"role":"user","content":"手机 13812345678 请回复"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := RedactJSON("openai", body, m, cfg, WalkOptions{Format: "openai"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamRestore(b *testing.B) {
	m, err := NewMapping(1024, 1<<20)
	if err != nil {
		b.Fatal(err)
	}
	raw := "sk-abcdefghijklmnop1234"
	ph, err := m.Placeholder(raw)
	if err != nil {
		b.Fatal(err)
	}
	chunk := []byte(`data: {"delta":{"content":"` + ph + ` mid text"}}` + "\n\n")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := NewStreamRestorer(m)
		out, drop := r.RestoreChunk(chunk)
		if drop || len(out) == 0 {
			b.Fatal("还原失败")
		}
	}
}
