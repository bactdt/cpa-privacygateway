package redact

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// 性质测试（design "Property 1/2"）：豁免候选不产生 entropy span，
// 敏感上下文的值被 secret_context span 精确覆盖。
// **Validates: Requirements 2.1–2.4, 2.6, 2.7**

const (
	detectPropSeed       = 20240612
	detectPropIterations = 300
)

var (
	propIDAlphabet  = []byte("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789")
	propHexAlphabet = []byte("0123456789abcdef")
	propValueExtra  = []byte("._-+/=@#!?%&*()+~^")
)

func propRandomBytes(rng *rand.Rand, alphabet []byte, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	return b.String()
}

// propRandomID 生成含数字、满足 tokenPattern 且熵足够的随机 id。
func propRandomID(rng *rand.Rand, n int) string {
	for {
		s := propRandomBytes(rng, propIDAlphabet, n)
		if strings.ContainsAny(s, "0123456789") {
			return s
		}
	}
}

func propRandomUUID(rng *rand.Rand) string {
	hex := propRandomBytes(rng, propHexAlphabet, 32)
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

func propSpansOfKind(spans []span, kind Kind) []span {
	var out []span
	for _, s := range spans {
		if s.kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func propOverlaps(start, end int, spans []span) bool {
	for _, s := range spans {
		if start < s.end && s.start < end {
			return true
		}
	}
	return false
}

// TestDetectPropertyExemptIDs：UUID、常见扩展名文件名、安全键值、配置前缀
// 即使满足高熵条件也不产生 entropy span；同时确认候选确实会命中高熵规则。
func TestDetectPropertyExemptIDs(t *testing.T) {
	rng := rand.New(rand.NewSource(detectPropSeed))
	for iter := 0; iter < detectPropIterations; iter++ {
		cfg := DefaultConfig()
		var token, text string
		switch rng.Intn(4) {
		case 0:
			for {
				token = propRandomUUID(rng)
				text = "id " + token + " done"
				if len(appendEntropy(text, 0, cfg, nil, nil)) > 0 {
					break
				}
			}
		case 1:
			ext := commonFileExtensions[rng.Intn(len(commonFileExtensions))]
			token = propRandomID(rng, 28)
			text = "file " + token + "." + ext + " done"
		case 2:
			key := []string{"process_id", "pid", "trace_id", "request_id"}[rng.Intn(4)]
			token = propRandomID(rng, 26)
			text = "cfg " + key + "=" + token + " done"
		case 3:
			token = "proc_" + propRandomID(rng, 26)
			cfg.SafeIDPrefixes = []string{"proc_"}
			text = "job " + token + " done"
		}
		start := strings.Index(text, token)
		if start < 0 {
			t.Fatalf("iter=%d: token %q not in %q", iter, token, text)
		}
		end := start + len(token)

		raw := appendEntropy(text, 0, cfg, nil, nil)
		if !propOverlaps(start, end, raw) {
			t.Fatalf("iter=%d: candidate is not high-entropy: token=%q text=%q spans=%v", iter, token, text, raw)
		}
		spans, _ := cfg.Detect(text, 0, nil)
		entropy := propSpansOfKind(spans, KindEntropy)
		if propOverlaps(start, end, entropy) {
			t.Fatalf("iter=%d: exempt token flagged as entropy: token=%q text=%q spans=%v", iter, token, text, entropy)
		}
	}
}

// TestDetectPropertySensitiveContext：敏感键名的值被 secret_context 精确覆盖；
// 非敏感键名不产生 secret_context。
func TestDetectPropertySensitiveContext(t *testing.T) {
	sensitiveKeys := []string{
		"DB_PASSWORD", "apiKey", "ACCESS_KEY_ID", "client-secret", "DB_PWD",
		"TOKEN_TTL", "MY_SECRET", "PRIVATE_KEY", "user_credential", "HEROKU_API_KEY",
	}
	plainKeys := []string{"user_name", "host", "region", "count", "session_id", "process_id"}
	rng := rand.New(rand.NewSource(detectPropSeed + 1))
	for iter := 0; iter < detectPropIterations; iter++ {
		key := sensitiveKeys[rng.Intn(len(sensitiveKeys))]
		sensitive := true
		if rng.Intn(4) == 0 {
			key = plainKeys[rng.Intn(len(plainKeys))]
			sensitive = false
		}
		value := propRandomValue(rng, 8+rng.Intn(23))
		var text string
		var start int
		switch rng.Intn(4) {
		case 0:
			text = "cfg " + key + "=" + value + " end"
			start = len("cfg ") + len(key) + 1
		case 1:
			text = "export " + key + "=" + value + " end"
			start = len("export ") + len(key) + 1
		case 2:
			text = `cfg "` + key + `": "` + value + `" end`
			start = len(`cfg "`) + len(key) + len(`": "`)
		default:
			text = "cfg " + key + ": " + value + " end"
			start = len("cfg ") + len(key) + 2
		}
		end := start + len(value)
		if text[start:end] != value {
			t.Fatalf("iter=%d: bad fixture text=%q value=%q", iter, text, value)
		}
		cfg := DefaultConfig()
		spans, _ := cfg.Detect(text, 0, nil)
		context := propSpansOfKind(spans, KindSecretContext)
		found := false
		for _, s := range context {
			if s.start == start && s.end == end {
				found = true
			}
		}
		if sensitive && !found {
			t.Fatalf("iter=%d: missing secret_context for %q: text=%q spans=%v", iter, value, text, context)
		}
		if !sensitive && propOverlaps(start, end, context) {
			t.Fatalf("iter=%d: plain key got secret_context: text=%q spans=%v", iter, text, context)
		}
	}
}

func propRandomValue(rng *rand.Rand, n int) string {
	for {
		var b strings.Builder
		for i := 0; i < n; i++ {
			if rng.Intn(8) == 0 {
				b.WriteByte(propValueExtra[rng.Intn(len(propValueExtra))])
				continue
			}
			b.WriteByte(propIDAlphabet[rng.Intn(len(propIDAlphabet))])
		}
		s := b.String()
		if strings.ContainsAny(s, "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ") {
			return s
		}
	}
}

func propOpenAISSEText(eff string) (string, []string, error) {
	return propSSEText(eff, func(v map[string]any) (string, bool, bool) {
		return openAIContent(v), true, false
	})
}

func propAntigravityText(eff string) (string, []string, error) {
	var v struct {
		Response struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(eff), &v); err != nil {
		return "", nil, err
	}
	var b strings.Builder
	for _, c := range v.Response.Candidates {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil, nil
}

func propInteractionsText(eff string) (string, []string, error) {
	return propSSEText(eff, func(v map[string]any) (string, bool, bool) {
		if v["event_type"] != "step.delta" {
			return "", false, false
		}
		delta, _ := v["delta"].(map[string]any)
		if delta["type"] != "text" {
			return "", false, false
		}
		s, _ := delta["text"].(string)
		return s, true, false
	})
}

func propCodexText(eff string) (string, []string, error) {
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
}

// propNewProtocols 是 design 新增的四个暂存组合（openai SSE、interactions、
// codex 完整帧、antigravity 裸 JSON）。
func propNewProtocols() []propProtocol {
	return []propProtocol{
		{
			name: "openai-sse", format: formatOpenAI,
			build: func(_ *rand.Rand, pieces []string, _ string) []string {
				out := make([]string, 0, len(pieces)+1)
				for _, piece := range pieces {
					out = append(out, "data: "+propOpenAIChunk(piece, false)+"\n\n")
				}
				return append(out, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			},
			extract: propOpenAISSEText,
		},
		{
			name: "interactions-sse", format: formatInteractions,
			build: func(_ *rand.Rand, pieces []string, _ string) []string {
				out := make([]string, 0, len(pieces)+1)
				for _, piece := range pieces {
					raw, _ := json.Marshal(map[string]any{
						"event_type": "step.delta",
						"index":      0,
						"delta":      map[string]any{"type": "text", "text": piece},
					})
					out = append(out, "event: step.delta\ndata: "+string(raw)+"\n\n")
				}
				return append(out, "event: done\ndata: [DONE]\n\n")
			},
			extract: propInteractionsText,
		},
		{
			name: "codex-sse-frames", format: formatCodex,
			build: func(_ *rand.Rand, pieces []string, full string) []string {
				out := make([]string, 0, len(pieces)+1)
				for _, piece := range pieces {
					out = append(out, propCodexDeltaEvt+"\n"+propCodexDelta(piece)+"\n\n")
				}
				return append(out, propCodexDoneEvt+"\n"+propCodexDone(full)+"\n\n")
			},
			extract: propCodexText,
		},
		{
			name: "antigravity-bare", format: formatAntigravity, bare: true,
			build: func(_ *rand.Rand, pieces []string, _ string) []string {
				out := make([]string, 0, len(pieces)+1)
				for _, piece := range pieces {
					out = append(out, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":`+jsonText(piece)+`}]},"index":0}]}}`)
				}
				return append(out, `{"response":{"candidates":[{"finishReason":"STOP","index":0}]}}`)
			},
			extract: propAntigravityText,
		},
	}
}

// TestStreamPropertyNewProtocols：新增组合各 200 组、切片 1–40 字节，
// 断言一进一出、JSON 合法、文本拼接 == 完整还原、结束后无暂扣。
func TestStreamPropertyNewProtocols(t *testing.T) {
	for _, proto := range propNewProtocols() {
		t.Run(proto.name, func(t *testing.T) {
			m, phs := newPropMapping(t)
			tokens := newPropTokens(m, phs)
			rng := rand.New(rand.NewSource(propSeed + 101))
			failures := 0
			var total StreamStats
			for iter := 0; iter < 200 && failures < propMaxFailures; iter++ {
				full := tokens.placeholderized(rng)
				pieces := propSplitN(rng, full, 40)
				chunks := proto.build(rng, pieces, full)
				st, err := runPropCase(proto, m, full, chunks)
				total.Carried += st.Carried
				total.Synthesized += st.Synthesized
				if err != nil {
					failures++
					t.Errorf("seed=%d iter=%d nonce=%s\nfull=%q\npieces=%q\nchunks=%q\n%v",
						propSeed+101, iter, m.Nonce(), full, pieces, chunks, err)
				}
			}
			if total.Carried == 0 || total.Synthesized == 0 {
				t.Errorf("property vacuous: carried=%d synthesized=%d", total.Carried, total.Synthesized)
			}
		})
	}
}
