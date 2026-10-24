package redact

// 非缺陷输入的保留性测试（bugfix tool-output-placeholder-leak，Property 7）。
// **Validates: Requirements 3.1–3.14**
//
// 这些用例在未修复代码上必须通过；修复后仍需通过，用于证明新豁免逻辑
// 只影响高熵误报，不放走凭据/PII、不改变既有配置语义。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

const preserveAlpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func preserveToken(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = preserveAlpha[rng.Intn(len(preserveAlpha))]
	}
	b[0] = preserveAlpha[rng.Intn(52)]
	if !strings.ContainsAny(string(b), "0123456789") {
		b[n-1] = byte('0' + rng.Intn(10))
	}
	return string(b)
}

func preserveDigitString(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + rng.Intn(10))
	}
	return string(b)
}

type preserveSample struct {
	text string
	kind Kind
}

// preserveSamples 每轮生成一组互不相同的凭据/PII 样例。
func preserveSamples(rng *rand.Rand, round int) []preserveSample {
	jwt := "eyJ" + preserveToken(rng, 10) + "." + preserveToken(rng, 10) + "." + preserveToken(rng, 10)
	phone := "139" + preserveDigitString(rng, 8)
	email := "u" + strings.ToLower(preserveToken(rng, 5)) + fmt.Sprintf("%d", round) + "@example.com"
	return []preserveSample{
		{text: "sk-" + preserveToken(rng, 24), kind: KindCredential},
		{text: "ghp_" + preserveToken(rng, 24), kind: KindCredential},
		{text: "github_pat_" + preserveToken(rng, 24), kind: KindCredential},
		{text: "AKIA" + strings.ToUpper(preserveToken(rng, 16)), kind: KindCredential},
		{text: "ASIA" + strings.ToUpper(preserveToken(rng, 16)), kind: KindCredential},
		{text: "AIza" + preserveToken(rng, 35), kind: KindCredential},
		{text: "xoxb-" + preserveToken(rng, 12), kind: KindCredential},
		{text: jwt, kind: KindCredential},
		{text: "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----", kind: KindCredential},
		{text: email, kind: KindEmail},
		{text: phone, kind: KindPhone},
		{text: "11010519491231002X", kind: KindIDCard},
		{text: "4111 1111 1111 1111", kind: KindBankCard},
	}
}

func TestPreserveCredentialAndPII(t *testing.T) {
	rng := rand.New(rand.NewSource(20261012))
	words := []string{"alpha", "beta", "gamma", "delta", "omega", "text", "note", "value"}
	cfg := DefaultConfig()
	for round := 0; round < 300; round++ {
		samples := preserveSamples(rng, round)
		chosen := map[string]preserveSample{}
		count := 1 + rng.Intn(4)
		for len(chosen) < count {
			s := samples[rng.Intn(len(samples))]
			chosen[s.text] = s
		}
		var parts []string
		want := map[string]Kind{}
		for sample := range chosen {
			want[sample] = chosen[sample].kind
			parts = append(parts, words[rng.Intn(len(words))], sample)
		}
		parts = append(parts, words[rng.Intn(len(words))])
		text := strings.Join(parts, " ")

		spans, _ := cfg.Detect(text, 0, nil)
		got := map[string]Kind{}
		for _, s := range spans {
			if s.allow || s.kind == KindEntropy {
				continue
			}
			got[text[s.start:s.end]] = s.kind
		}
		if len(got) != len(want) {
			t.Fatalf("round %d：非熵检出集合 = %v，want %v\n文本：%s", round, got, want, text)
		}
		for raw, kind := range want {
			if got[raw] != kind {
				t.Fatalf("round %d：%q 检出为 %q，want %q（全部 %v）", round, raw, got[raw], kind, got)
			}
		}
	}
}

func TestPreserveEntropyStillHits(t *testing.T) {
	rng := rand.New(rand.NewSource(20261013))
	cfg := DefaultConfig()
	pathToken := preserveToken(rng, 28)
	cases := []struct{ text, want string }{
		{preserveToken(rng, 32), ""},
		{"https://example.com/" + pathToken, pathToken},
		{"sk_live_" + preserveToken(rng, 24), ""},
		{"glpat-" + preserveToken(rng, 24), ""},
		{"hf_" + preserveToken(rng, 28), ""},
		{"npm_" + preserveToken(rng, 28), ""},
	}
	for _, tc := range cases {
		token := tc.text
		if tc.want != "" {
			token = tc.want
		}
		text := "value " + tc.text + " end"
		spans, _ := cfg.Detect(text, 0, nil)
		found := false
		for _, s := range spans {
			if s.kind == KindEntropy && text[s.start:s.end] == token {
				found = true
			}
		}
		if !found {
			t.Fatalf("高熵 token 未检出：%q -> %v", token, spans)
		}
	}
}

func TestPreserveConfigSemantics(t *testing.T) {
	rng := rand.New(rand.NewSource(20261014))
	m := newTestMapping(t, 0, 0)

	email := "keep" + preserveToken(rng, 4) + "@example.com"
	cfg := DefaultConfig()
	cfg.Allowlist = []string{email}
	out, count, err := m.Redact("mail "+email+" end", 0, cfg)
	if err != nil || count != 0 || out != "mail "+email+" end" {
		t.Fatalf("allowlist 语义改变：out=%q count=%d err=%v", out, count, err)
	}

	cfg = DefaultConfig()
	cfg.CustomRules = []string{`ACME-\d{4}`}
	out, count, err = m.Redact("id ACME-1234 end", 0, cfg)
	if err != nil || count != 1 || !strings.Contains(out, placeholderOpen) {
		t.Fatalf("custom_rules 语义改变：out=%q count=%d err=%v", out, count, err)
	}

	cfg = DefaultConfig()
	cfg.Entropy = false
	token := preserveToken(rng, 32)
	spans, _ := cfg.Detect("value "+token+" end", 0, nil)
	if len(spansOfKind("value "+token+" end", spans, KindEntropy)) != 0 {
		t.Fatalf("entropy:false 仍检出熵：%v", spans)
	}

	cfg = DefaultConfig()
	cfg.MinEntropyLen = 40
	spans, _ = cfg.Detect("value "+token+" end", 0, nil)
	if len(spansOfKind("value "+token+" end", spans, KindEntropy)) != 0 {
		t.Fatalf("min_entropy_len=40 仍检出短 token：%v", spans)
	}
}

func TestPreserveNonSensitiveKV(t *testing.T) {
	rng := rand.New(rand.NewSource(20261015))
	m := newTestMapping(t, 0, 0)
	cfg := DefaultConfig()

	token := preserveToken(rng, 30)
	out, count, err := m.Redact("BUILD_ID="+token, 0, cfg)
	if err != nil || count != 1 || !strings.Contains(out, placeholderOpen) {
		t.Fatalf("BUILD_ID 高熵不再检出：out=%q count=%d err=%v", out, count, err)
	}

	for _, text := range []string{
		"LOG_FILE=app.log",
		"TOKEN_TTL=3600",
		"ENABLE_TOKEN=true",
		"DB_PASSWORD=short",
		"input_tokens=12345",
	} {
		out, count, err := m.Redact(text, 0, cfg)
		if err != nil || count != 0 || out != text {
			t.Fatalf("非敏感 kv 被改写：%q -> %q count=%d err=%v", text, out, count, err)
		}
	}
}

func TestPreserveOtherOutputFields(t *testing.T) {
	email := "nokeep@example.com"
	bodies := []string{
		`{"metadata":{"output":"` + email + `"},"input":[{"type":"message","role":"user","output":"` + email + `"}]}`,
		`{"output":"` + email + `"}`,
	}
	formats := []string{"openai-response", "codex", "openai", "claude"}
	for _, format := range formats {
		for _, body := range bodies {
			m := newTestMapping(t, 0, 0)
			out, count, err := RedactJSON(format, []byte(body), m, DefaultConfig(), WalkOptions{Format: format})
			if err != nil {
				t.Fatalf("%s RedactJSON: %v", format, err)
			}
			if count != 0 || string(out) != body {
				t.Fatalf("%s 非工具 output 被改写：count=%d out=%s", format, count, out)
			}
		}
	}
}

func TestPreserveRedactJSONBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(20261016))
	values := []string{
		"plain text",
		"user@example.com",
		"13812345678",
		"中文内容",
		"[[PG_ffffffffffffffff_1]]",
		"sk-" + preserveToken(rng, 24),
		preserveToken(rng, 32),
	}
	keys := []string{"text", "content", "prompt", "instructions", "arguments", "output"}
	for i := 0; i < 200; i++ {
		body := map[string]any{
			"type":    "message",
			"text":    values[rng.Intn(len(values))],
			"content": values[rng.Intn(len(values))],
			"metadata": map[string]any{
				"output": values[rng.Intn(len(values))],
			},
		}
		for _, key := range keys {
			if rng.Intn(2) == 0 {
				body[key] = values[rng.Intn(len(values))]
			}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		m := newTestMapping(t, 0, 0)
		base, _, err := RedactJSON("openai-response", raw, m, DefaultConfig(), WalkOptions{Format: "openai-response"})
		if err != nil {
			t.Fatalf("case %d baseline: %v", i, err)
		}
		observed, _, err := RedactJSON("openai-response", raw, m, DefaultConfig(), WalkOptions{
			Format:    "openai-response",
			OnFinding: func(Finding) {},
		})
		if err != nil {
			t.Fatalf("case %d observed: %v", i, err)
		}
		if !bytes.Equal(base, observed) {
			t.Fatalf("case %d nil hook 改变了输出字节：\nbase %s\nobs  %s", i, base, observed)
		}
	}
}
