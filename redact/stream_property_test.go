package redact

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// 性质测试（design "Property-Based Tests"）：四协议宿主形态 × 随机原文 × 随机切分。
// **Validates: Requirements 2.1, 2.4, 2.5, 3.5**

const (
	propSeed        = 20240611
	propIterations  = 250
	propMaxFailures = 3
	propOtherNonce  = "ffffffffffffffff"
)

var propRaws = []string{"user@example.com", "13812345678", "sk-abcdefghijklmnop1234"}

var propWords = []string{"邮箱", "手机", "结束", "你好", "hello", "world", "API", "key:", " ", "，", "\n", `"q"`, `\`, "😀", "<&>"}

// propProtocol 描述一个协议的宿主形态：如何由文本片段构造块、如何从有效输出提取文本。
type propProtocol struct {
	name   string
	format string
	bare   bool // 块是裸 JSON（否则为 SSE 文本）
	build  func(rng *rand.Rand, pieces []string, full string) []string
	// extract 返回本块有效输出中目标文本流的增量文本，以及应等于完整还原原文的全文字段。
	extract func(eff string) (delta string, full []string, err error)
}

func propOpenAIChunk(piece string, finish bool) string {
	reason := "null"
	if finish {
		reason = `"stop"`
	}
	return `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":` + jsonText(piece) + `},"finish_reason":` + reason + `}]}`
}

func propGeminiChunk(piece string, finish bool) string {
	reason := ""
	if finish {
		reason = `,"finishReason":"STOP"`
	}
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + jsonText(piece) + `}]}` + reason + `,"index":0}]}`
}

const (
	propClaudeStop    = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	propCodexDeltaEvt = "event: response.output_text.delta"
	propCodexDoneEvt  = "event: response.output_text.done"
)

func propCodexDelta(piece string) string {
	return `data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":` + jsonText(piece) + `}`
}

func propCodexDone(full string) string {
	return `data: {"type":"response.output_text.done","item_id":"m1","output_index":0,"content_index":0,"text":` + jsonText(full) + `}`
}

// buildBare 构造裸 JSON 块序列：随机地让最后一个片段与结束信号同块，或单独追加结束块。
func buildBare(chunk func(string, bool) string, finishOnly string) func(*rand.Rand, []string, string) []string {
	return func(rng *rand.Rand, pieces []string, _ string) []string {
		out := make([]string, 0, len(pieces)+1)
		inline := rng.Intn(2) == 0
		for i, piece := range pieces {
			out = append(out, chunk(piece, inline && i == len(pieces)-1))
		}
		if !inline {
			out = append(out, finishOnly)
		}
		return out
	}
}

func propProtocols() []propProtocol {
	return []propProtocol{
		{
			name: "openai-bare", format: formatOpenAI, bare: true,
			build: buildBare(propOpenAIChunk, `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			extract: func(eff string) (string, []string, error) {
				var v map[string]any
				if err := json.Unmarshal([]byte(eff), &v); err != nil {
					return "", nil, err
				}
				return openAIContent(v), nil, nil
			},
		},
		{
			name: "gemini-bare", format: formatGemini, bare: true,
			build:   buildBare(propGeminiChunk, `{"candidates":[{"finishReason":"STOP","index":0}]}`),
			extract: propGeminiText,
		},
		{
			name: "claude-frames", format: formatClaude,
			build: func(rng *rand.Rand, pieces []string, _ string) []string {
				out := make([]string, 0, len(pieces)+2)
				for _, piece := range pieces {
					out = append(out, claudeDelta(piece))
				}
				out = append(out, propClaudeStop)
				if rng.Intn(2) == 0 {
					out = append(out, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}
				return out
			},
			extract: func(eff string) (string, []string, error) {
				return propSSEText(eff, func(v map[string]any) (string, bool, bool) {
					if v["type"] != "content_block_delta" {
						return "", false, false
					}
					delta, _ := v["delta"].(map[string]any)
					s, _ := delta["text"].(string)
					return s, true, false
				})
			},
		},
		{
			name: "codex-lines", format: formatResponses,
			build: func(_ *rand.Rand, pieces []string, full string) []string {
				out := make([]string, 0, 2*len(pieces)+2)
				for _, piece := range pieces {
					out = append(out, propCodexDeltaEvt, propCodexDelta(piece))
				}
				return append(out, propCodexDoneEvt, propCodexDone(full))
			},
			extract: func(eff string) (string, []string, error) {
				return propSSEText(eff, func(v map[string]any) (string, bool, bool) {
					switch v["type"] {
					case "response.output_text.delta":
						s, _ := v["delta"].(string)
						return s, true, false
					case "response.output_text.done":
						s, _ := v["text"].(string)
						return s, true, true
					}
					return "", false, false
				})
			},
		},
	}
}

func propGeminiText(eff string) (string, []string, error) {
	var v struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(eff), &v); err != nil {
		return "", nil, err
	}
	var b strings.Builder
	for _, c := range v.Candidates {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil, nil
}

// propDataPayloads 与 restoreSSE 同样按 `\n` 切行、去 `\r`，返回全部 data: 载荷；
// 非 [DONE] 的非法 JSON 载荷返回错误。
func propDataPayloads(text string) ([]string, error) {
	var payloads []string
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), "data:")
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(rest, " ")
		if rest != "[DONE]" && !json.Valid([]byte(rest)) {
			return nil, fmt.Errorf("invalid data payload %q", rest)
		}
		payloads = append(payloads, rest)
	}
	return payloads, nil
}

// propSSEText 解析 SSE 有效输出全部 data: 载荷，pick 返回 (文本, 是否目标, 是否全文字段)。
func propSSEText(eff string, pick func(map[string]any) (string, bool, bool)) (string, []string, error) {
	payloads, err := propDataPayloads(eff)
	if err != nil {
		return "", nil, err
	}
	var delta strings.Builder
	var full []string
	for _, p := range payloads {
		if p == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(p), &v); err != nil {
			return "", nil, err
		}
		s, ok, isFull := pick(v)
		switch {
		case !ok:
		case isFull:
			full = append(full, s)
		default:
			delta.WriteString(s)
		}
	}
	return delta.String(), full, nil
}

// propTokens 是一次迭代可用的原文片段：普通词、本请求占位符与各种假前缀。
type propTokens struct {
	phs   []string
	fakes []string
}

func newPropTokens(m *Mapping, phs []string) propTokens {
	nonce := m.Nonce()
	return propTokens{
		phs: phs,
		fakes: []string{
			"[", "]]", "[[", "[[PG_", "[[PG_" + propOtherNonce + "_1]]",
			"[[PG_" + nonce + "_", "[[PG_" + nonce, "[[PG_" + nonce + "_99]]", "[[PG_" + nonce + "_1]",
		},
	}
}

// placeholderized 生成已占位符化的随机原文。
func (p propTokens) placeholderized(rng *rand.Rand) string {
	var b strings.Builder
	n := 1 + rng.Intn(14)
	for i := 0; i < n; i++ {
		switch r := rng.Intn(10); {
		case r < 4:
			b.WriteString(propWords[rng.Intn(len(propWords))])
		case r < 7:
			b.WriteString(p.phs[rng.Intn(len(p.phs))])
		default:
			b.WriteString(p.fakes[rng.Intn(len(p.fakes))])
		}
	}
	return b.String()
}

// propSplit 按 1..12 字节随机切分，切点对齐到 rune 边界（每片至少一个 rune）。
func propSplit(rng *rand.Rand, text string) []string {
	var pieces []string
	for len(text) > 0 {
		cut := 1 + rng.Intn(12)
		if cut >= len(text) {
			pieces = append(pieces, text)
			break
		}
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		pieces = append(pieces, text[:cut])
		text = text[cut:]
	}
	return pieces
}

func newPropMapping(t *testing.T) (*Mapping, []string) {
	t.Helper()
	m := newTestMapping(t, 0, 0)
	if m.Nonce() == propOtherNonce {
		t.Skip("nonce collides with fake nonce")
	}
	phs := make([]string, 0, len(propRaws))
	for _, raw := range propRaws {
		ph, err := m.Placeholder(raw)
		if err != nil {
			t.Fatalf("Placeholder: %v", err)
		}
		phs = append(phs, ph)
	}
	return m, phs
}

// TestStreamPropertyRandomSplit：块数不变且从不丢弃、每个 JSON 合法、
// 目标文本拼接 == Mapping.Restore(占位符化原文)、结束后无暂扣。
func TestStreamPropertyRandomSplit(t *testing.T) {
	for _, proto := range propProtocols() {
		t.Run(proto.name, func(t *testing.T) {
			m, phs := newPropMapping(t)
			tokens := newPropTokens(m, phs)
			rng := rand.New(rand.NewSource(propSeed))
			failures := 0
			var total StreamStats
			for iter := 0; iter < propIterations && failures < propMaxFailures; iter++ {
				full := tokens.placeholderized(rng)
				pieces := propSplit(rng, full)
				chunks := proto.build(rng, pieces, full)
				st, err := runPropCase(proto, m, full, chunks)
				total.Carried += st.Carried
				total.Synthesized += st.Synthesized
				if err != nil {
					failures++
					t.Errorf("seed=%d iter=%d nonce=%s\nfull=%q\npieces=%q\nchunks=%q\n%v",
						propSeed, iter, m.Nonce(), full, pieces, chunks, err)
				}
			}
			// 防止性质空转：随机输入必须实际触发跨事件暂扣与结束投递。
			if total.Carried == 0 || total.Synthesized == 0 {
				t.Errorf("property vacuous: carried=%d synthesized=%d", total.Carried, total.Synthesized)
			}
			t.Logf("carried=%d synthesized=%d", total.Carried, total.Synthesized)
		})
	}
}

func runPropCase(proto propProtocol, m *Mapping, full string, chunks []string) (StreamStats, error) {
	want := m.Restore(full)
	s := NewStreamRestorer(m)
	effs := make([]string, 0, len(chunks))
	var got strings.Builder
	for i, chunk := range chunks {
		out, changed := s.RestorePayload(proto.format, []byte(chunk))
		if !changed && string(out) != chunk {
			return s.Stats(), fmt.Errorf("chunk %d: changed=false but out %q != in", i, out)
		}
		eff := string(out)
		effs = append(effs, eff)
		if eff == "" {
			return s.Stats(), fmt.Errorf("chunk %d dropped (empty output) for %q", i, chunk)
		}
		if proto.bare && !json.Valid(out) {
			return s.Stats(), fmt.Errorf("chunk %d: invalid bare JSON %q", i, eff)
		}
		// 不含文本字段的帧/行（结束帧、event: 行）只允许在前面插合成帧，自身逐字节保留。
		textless := chunk == propClaudeStop || strings.Contains(chunk, "message_stop") ||
			(strings.HasPrefix(chunk, "event:") && !strings.Contains(chunk, "\n"))
		if textless && !strings.HasSuffix(eff, chunk) {
			return s.Stats(), fmt.Errorf("chunk %d: non-data chunk not preserved at tail: %q -> %q", i, chunk, eff)
		}
		delta, fulls, err := proto.extract(eff)
		if err != nil {
			return s.Stats(), fmt.Errorf("chunk %d: %v in %q", i, err, eff)
		}
		for _, f := range fulls {
			if f != want {
				return s.Stats(), fmt.Errorf("chunk %d: full text field %q, want %q", i, f, want)
			}
		}
		got.WriteString(delta)
	}
	if len(effs) != len(chunks) {
		return s.Stats(), fmt.Errorf("chunk count %d != %d", len(effs), len(chunks))
	}
	if got.String() != want {
		return s.Stats(), fmt.Errorf("concat mismatch\n got=%q\nwant=%q\neffs=%q", got.String(), want, effs)
	}
	if s.Pending() != 0 {
		return s.Stats(), fmt.Errorf("pending=%d after end signal; effs=%q", s.Pending(), effs)
	}
	return s.Stats(), nil
}

// propSafeText 生成不含本 nonce 占位符、也不以可暂扣前缀结尾的文本。
func propSafeText(rng *rand.Rand) string {
	safe := append(append([]string(nil), propWords...), "[ok]", "[[PG_"+propOtherNonce+"_1]]", "[[x]]")
	var b strings.Builder
	n := 1 + rng.Intn(4)
	for i := 0; i < n; i++ {
		b.WriteString(safe[rng.Intn(len(safe))])
	}
	return b.String()
}

// propSafeChunks 生成某协议下无占位符的随机事件序列（夹杂 [DONE]、注释与结束信号）。
func propSafeChunks(rng *rand.Rand, format string) []string {
	n := 1 + rng.Intn(6)
	var out []string
	for i := 0; i < n; i++ {
		text := propSafeText(rng)
		switch rng.Intn(5) {
		case 0:
			out = append(out, "[DONE]")
			continue
		case 1:
			out = append(out, ": keep-alive\n\n")
			continue
		}
		switch format {
		case formatOpenAI:
			out = append(out, propOpenAIChunk(text, rng.Intn(4) == 0))
		case formatGemini:
			out = append(out, propGeminiChunk(text, rng.Intn(4) == 0))
		case formatClaude:
			out = append(out, claudeDelta(text))
			if rng.Intn(4) == 0 {
				out = append(out, propClaudeStop)
			}
		default:
			out = append(out, propCodexDeltaEvt, propCodexDelta(text))
			if rng.Intn(4) == 0 {
				out = append(out, propCodexDoneEvt, propCodexDone(text))
			}
		}
	}
	return out
}

// TestStreamPropertyNoPlaceholderUnchanged：无占位符且无暂扣的事件 changed=false 且返回原切片。
func TestStreamPropertyNoPlaceholderUnchanged(t *testing.T) {
	for _, format := range []string{formatOpenAI, formatGemini, formatClaude, formatResponses} {
		t.Run(format, func(t *testing.T) {
			m, _ := newPropMapping(t)
			rng := rand.New(rand.NewSource(propSeed))
			failures := 0
			for iter := 0; iter < propIterations && failures < propMaxFailures; iter++ {
				s := NewStreamRestorer(m)
				chunks := propSafeChunks(rng, format)
				for i, chunk := range chunks {
					in := []byte(chunk)
					out, changed := s.RestorePayload(format, in)
					if changed || len(out) != len(in) || &out[0] != &in[0] || s.Pending() != 0 {
						failures++
						t.Errorf("seed=%d iter=%d chunk %d: changed=%v pending=%d\nin =%q\nout=%q\nchunks=%q",
							propSeed, iter, i, changed, s.Pending(), chunk, out, chunks)
						break
					}
				}
			}
		})
	}
}
