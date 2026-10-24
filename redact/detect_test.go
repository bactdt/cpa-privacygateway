package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectCredentials(t *testing.T) {
	cases := map[string]string{
		"openai key":  "sk-abcdefghijklmnop1234",
		"github pat":  "ghp_" + strings.Repeat("A", 24),
		"aws key":     "AKIA" + strings.Repeat("B", 16),
		"google key":  "AIza" + strings.Repeat("C", 35),
		"slack token": "xoxb-" + strings.Repeat("d", 20),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			spans, _ := cfg.Detect(value, 0, nil)
			if len(spans) == 0 {
				t.Fatalf("%s 未被检测：%q", name, value)
			}
			if spans[0].kind != KindCredential {
				t.Errorf("kind = %s, want %s", spans[0].kind, KindCredential)
			}
			if value[spans[0].start:spans[0].end] != value {
				t.Errorf("span 未覆盖原文：%q", value[spans[0].start:spans[0].end])
			}
		})
	}
}

func TestDetectDisabledCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Credentials = false
	if spans, _ := cfg.Detect("sk-abcdefghijklmnop1234", 0, nil); len(spans) != 0 {
		t.Errorf("credentials 关闭时不应检出：%v", spans)
	}
}

func TestDetectEmail(t *testing.T) {
	cfg := DefaultConfig()
	text := "mail me at user@example.com thanks"
	spans, _ := cfg.Detect(text, 0, nil)
	var found bool
	for _, s := range spans {
		if s.kind == KindEmail {
			found = true
			if got := text[s.start:s.end]; got != "user@example.com" {
				t.Errorf("email span = %q", got)
			}
		}
	}
	if !found {
		t.Error("email 未被检出")
	}
}

func TestDetectPhone(t *testing.T) {
	cfg := DefaultConfig()
	text := "call 13800138000 now"
	spans, _ := cfg.Detect(text, 0, nil)
	var found bool
	for _, s := range spans {
		if s.kind == KindPhone {
			found = true
			if got := text[s.start:s.end]; got != "13800138000" {
				t.Errorf("phone span = %q", got)
			}
		}
	}
	if !found {
		t.Error("phone 未被检出")
	}
}

func TestDetectIDCard(t *testing.T) {
	cfg := DefaultConfig()
	valid := "110105194912310011"
	if !validIDCard(valid) {
		t.Fatalf("测试数据 %s 应为有效身份证", valid)
	}
	text := "id " + valid
	spans, _ := cfg.Detect(text, 0, nil)
	var found bool
	for _, s := range spans {
		if s.kind == KindIDCard {
			found = true
			if got := text[s.start:s.end]; got != valid {
				t.Errorf("id span = %q", got)
			}
		}
	}
	if !found {
		t.Error("有效身份证未被检出")
	}
}

// 现状特征（TD-CPA-028）：以 X 结尾的身份证号当前不被检出。
// idCardCandidates 的数字串扫描止于首个非数字字符（含校验位 X），
// 使 end-i 为 17 而非 18，从而 validIDCard 无法通过。
// 此处锁定现状，修复时须同步改为"含 X 校验位"并单独提交（规则 R1/R2）。
func TestDetectIDCardXSuffix(t *testing.T) {
	cfg := DefaultConfig()
	xfinal := "11010519491231002X"
	if !validIDCard(xfinal) {
		t.Fatalf("%s 应通过 validIDCard（其支持 X 校验位）", xfinal)
	}
	spans, _ := cfg.Detect("id "+xfinal, 0, nil)
	var found []string
	for _, s := range spans {
		if s.kind == KindIDCard {
			found = append(found, ("id " + xfinal)[s.start:s.end])
		}
	}
	if len(found) != 1 || found[0] != xfinal {
		t.Fatalf("X 结尾身份证应检出一次: %v", found)
	}
	if spans, _ := cfg.Detect("id "+xfinal+"a", 0, nil); len(spans) > 0 {
		t.Errorf("X 后跟字母不应检出: %v", spans)
	}
}

func TestDetectInvalidIDCardRejected(t *testing.T) {
	if validIDCard("110105194912310021") {
		t.Error("校验位错误的身份证不应通过")
	}
	if validIDCard("short") {
		t.Error("长度不足不应通过")
	}
}

func TestDetectBankCard(t *testing.T) {
	cfg := DefaultConfig()
	valid := "4111111111111111"
	if !luhn(normalizeDigits(valid)) {
		t.Fatalf("测试数据 %s 应通过 Luhn", valid)
	}
	text := "cc=" + valid + "&x=1"
	spans, _ := cfg.Detect(text, 0, nil)
	var found bool
	for _, s := range spans {
		if s.kind == KindBankCard {
			found = true
		}
	}
	if !found {
		t.Errorf("有效银行卡未被检出：%v", spans)
	}
}

// 现状特征（TD-CPA-029）：前一字符为空格或连字符的银行卡号当前不被检出。
// bankCardCandidates 在扫描前跳过 i-1 为 ' ' 或 '-' 的起始位，
// 导致 "card 4111..." 形式漏检；而 "cc=4111..."、行首数字可正常检出。
// 此处锁定现状，修复时须单独提交（规则 R1/R2）。
func TestDetectPhoneBoundary(t *testing.T) {
	cfg := DefaultConfig()
	// 反例：长数字串中的 11 位片段不是手机号。
	for _, text := range []string{
		"ts=1728546241000",
		"order=1231385962410001",
		"id=138123456789",
	} {
		for _, s := range func() []span { sp, _ := cfg.Detect(text, 0, nil); return sp }() {
			if s.kind == KindPhone {
				t.Errorf("长数字串不应误报手机号: %s -> %q", text, text[s.start:s.end])
			}
		}
	}
	// 正例：独立手机号、86 前缀仍检出。
	for _, text := range []string{"tel 13812345678", "+8613812345678"} {
		var found int
		for _, s := range func() []span { sp, _ := cfg.Detect(text, 0, nil); return sp }() {
			if s.kind == KindPhone {
				found++
			}
		}
		if found != 1 {
			t.Errorf("手机号应检出一次: %s -> %d", text, found)
		}
	}
}

func TestDetectBankCardSpaceBefore(t *testing.T) {
	cfg := DefaultConfig()
	for _, text := range []string{"card 4111111111111111", "card-4111111111111111"} {
		var found int
		for _, s := range func() []span { sp, _ := cfg.Detect(text, 0, nil); return sp }() {
			if s.kind == KindBankCard {
				found++
			}
		}
		if found != 1 {
			t.Errorf("空格/连字符前缀银行卡应检出一次: %s -> %d", text, found)
		}
	}
}

func TestLuhn(t *testing.T) {
	if !luhn("4111111111111111") {
		t.Error("4111111111111111 应通过 Luhn")
	}
	if luhn("1234567890123456") {
		t.Error("1234567890123456 不应通过 Luhn")
	}
	if luhn("") {
		t.Error("空串不应通过 Luhn")
	}
}

func TestDetectEntropyHigh(t *testing.T) {
	cfg := DefaultConfig()
	value := "aB3dE5gH7jK9mN1pQ3sT5vW7"
	if len(value) < cfg.MinEntropyLen {
		t.Fatalf("测试数据长度 %d 不足 %d", len(value), cfg.MinEntropyLen)
	}
	if shannon(value) < cfg.MinEntropy {
		t.Fatalf("测试数据熵 %f 低于阈值", shannon(value))
	}
	spans, _ := cfg.Detect(value, 0, nil)
	var found bool
	for _, s := range spans {
		if s.kind == KindEntropy {
			found = true
		}
	}
	if !found {
		t.Errorf("高熵字符串未被检出（熵=%f）", shannon(value))
	}
}

func TestDetectEntropyLowLenIgnored(t *testing.T) {
	cfg := DefaultConfig()
	short := "abc1"
	spans, _ := cfg.Detect(short, 0, nil)
	for _, s := range spans {
		if s.kind == KindEntropy {
			t.Errorf("短字符串不应判为高熵：%q", short)
		}
	}
}

func TestDetectAllowlistExcludes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Allowlist = []string{"sk-abcdefghijklmnop1234"}
	text := "secret sk-abcdefghijklmnop1234 end"
	raw, _ := cfg.Detect(text, 0, nil)
	spans := resolveSpans(raw)
	if len(spans) != 0 {
		t.Errorf("白名单值应被排除，仍有 %d 个 span：%v", len(spans), spans)
	}
}

func TestDetectEmptyText(t *testing.T) {
	cfg := DefaultConfig()
	if spans, _ := cfg.Detect("", 0, nil); len(spans) != 0 {
		t.Errorf("空文本不应产生 span：%v", spans)
	}
}

func TestShannon(t *testing.T) {
	if shannon("") != 0 {
		t.Error("空串熵应为 0")
	}
	if shannon("aaaa") != 0 {
		t.Errorf("全同字符熵应为 0，实际 %f", shannon("aaaa"))
	}
	if shannon("ab") <= 0 {
		t.Errorf("ab 熵应 > 0，实际 %f", shannon("ab"))
	}
}

func TestResolveSpansSortsAndMerges(t *testing.T) {
	in := []span{
		{start: 10, end: 20, kind: KindEmail},
		{start: 0, end: 5, kind: KindPhone},
		{start: 3, end: 8, kind: KindPhone},
	}
	out := resolveSpans(in)
	if len(out) == 0 {
		t.Fatal("resolveSpans 返回空")
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].start > out[i].start {
			t.Errorf("结果未按 start 排序：%v", out)
		}
	}
}

func TestResolveSpansOverlapDedup(t *testing.T) {
	in := []span{
		{start: 0, end: 10, kind: KindEmail},
		{start: 5, end: 15, kind: KindPhone},
	}
	out := resolveSpans(in)
	if len(out) != 1 {
		t.Errorf("重叠 span 应归并为 1，实际 %d：%v", len(out), out)
	}
}

func TestResolveSpansEmpty(t *testing.T) {
	if got := resolveSpans(nil); got != nil {
		t.Errorf("空输入应返回 nil，实际 %v", got)
	}
}

func TestMaxFindingsTruncatedSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFindings = 1
	text := "a@b.com c@d.com e@f.com"
	_, truncated := cfg.Detect(text, 0, nil)
	if !truncated {
		t.Fatal("超限应报告 truncated")
	}
	cfg.MaxFindings = 3
	_, truncated = cfg.Detect(text, 0, nil)
	if truncated {
		t.Error("限额内不应报告 truncated")
	}
	cfg.MaxFindings = 0
	spans, truncated := cfg.Detect(text, 0, nil)
	if truncated || len(spans) != 3 {
		t.Errorf("MaxFindings<=0 应无上限且全检（3 处），实际 %d, truncated=%v", len(spans), truncated)
	}
}

func TestMaxFindingsAllowlistNotCounted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFindings = 3
	cfg.Allowlist = []string{"a@b.com"}
	// 3 个邮箱检出 + 1 条 allow 记录：恰好等于上限；若 allow 被计入则 4>3 触发 truncated
	text := "a@b.com c@d.com e@f.com"
	spans, truncated := cfg.Detect(text, 0, nil)
	if truncated {
		t.Error("allowlist 条目不应计入上限")
	}
	kept := 0
	for _, s := range spans {
		if !s.allow {
			kept++
		}
	}
	if kept != 3 {
		t.Errorf("3 个检出应全部保留，实际 %d", kept)
	}
}

func TestRedactJSONMaxFindingsError(t *testing.T) {
	m, err := NewMapping(1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxFindings = 1
	_, _, err = RedactJSON("openai", []byte(`{"content":"a@b.com c@d.com"}`), m, cfg, WalkOptions{Format: "openai"})
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("超限应返回 ErrFindings，实际 %v", err)
	}
}

func TestDetectMaxFindingsReportsTruncated(t *testing.T) {
	text := "a@b.com c@d.com e@f.com"
	cases := []struct {
		name        string
		maxFindings int
		wantTrunc   bool
		wantKept    int
	}{
		{"under limit", 10, false, 3},
		{"exact limit", 3, false, 3},
		{"over limit", 2, true, 2},
		{"over limit by one", 1, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxFindings = tc.maxFindings
			spans, truncated := cfg.Detect(text, 0, nil)
			if truncated != tc.wantTrunc {
				t.Errorf("max_findings=%d truncated=%v, want %v", tc.maxFindings, truncated, tc.wantTrunc)
			}
			if got := len(spans); got != tc.wantKept {
				t.Errorf("max_findings=%d 保留 %d，want %d", tc.maxFindings, got, tc.wantKept)
			}
		})
	}
	allow := DefaultConfig()
	allow.MaxFindings = 3
	allow.Allowlist = []string{"a@b.com"}
	spans, truncated := allow.Detect(text, 0, nil)
	if truncated {
		t.Error("allowlist 条目不应计入上限（3 检出 + 1 allow = 3 > 3 应不触发）")
	}
	kept := 0
	for _, s := range spans {
		if !s.allow {
			kept++
		}
	}
	if kept != 3 {
		t.Errorf("allow 不计数时 3 个检出应全保留，实际 %d", kept)
	}
}

func TestDetectExemptionsAndSecretContext(t *testing.T) {
	uuid := "a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b"
	proc := "proc_abcdefghijklmnopqrstuvwxyz01"
	pathToken := "abcdefghijklmnopqrstuvwx1234"
	cases := []struct {
		name       string
		text       string
		cfg        func() Config
		wantKind   Kind
		rejectKind Kind
	}{
		{"uuid exempt", "session " + uuid + " done", DefaultConfig, "", KindEntropy},
		{"filename body exempt", "saved workspace-snapshot-20261011-153045.tar.gz ok", DefaultConfig, "", KindEntropy},
		{"url path still entropy", "https://hooks.slack.com/services/T0/B0/" + pathToken, DefaultConfig, KindEntropy, ""},
		{"safe id key", "process_id: " + proc, DefaultConfig, "", KindEntropy},
		{"proc plain still entropy", "Started process " + proc, DefaultConfig, KindEntropy, ""},
		{"safe id prefix", "Started process " + proc, func() Config {
			c := DefaultConfig()
			c.SafeIDPrefixes = []string{"proc_"}
			return c
		}, "", KindEntropy},
		{"safe id prefix case sensitive", "Started process PROC_abcdefghijklmnopqrstuvwxyz01", func() Config {
			c := DefaultConfig()
			c.SafeIDPrefixes = []string{"proc_"}
			return c
		}, KindEntropy, ""},
		{"secret context wins over uuid", "HEROKU_API_KEY=" + uuid, DefaultConfig, KindSecretContext, KindEntropy},
		{"secret context low entropy", "DB_PASSWORD=Summer2024abc", DefaultConfig, KindSecretContext, ""},
		{"glpat still entropy", "token glpat-abcdefghijklmnopqrs1", DefaultConfig, KindEntropy, ""},
		{"stripe key still detected", "STRIPE_KEY=sk_live_abcdefghijklmnopqrs1", DefaultConfig, KindEntropy, ""},
		{"numeric sensitive value ignored", "TOKEN_TTL=3600", DefaultConfig, "", ""},
		{"boolean sensitive value ignored", "ENABLE_TOKEN=true", DefaultConfig, "", ""},
		{"pwd not sensitive", "PWD=/home/gh/project", DefaultConfig, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg()
			spans, _ := cfg.Detect(tc.text, 0, nil)
			if tc.wantKind != "" && len(spansOfKind(tc.text, spans, tc.wantKind)) == 0 {
				t.Fatalf("缺少 %s 检出：%v", tc.wantKind, spans)
			}
			if tc.rejectKind != "" {
				if got := spansOfKind(tc.text, spans, tc.rejectKind); len(got) != 0 {
					t.Fatalf("不应有 %s 检出：%v", tc.rejectKind, got)
				}
			}
		})
	}

	cfg := DefaultConfig()
	cfg.SecretContext = false
	text := "DB_PASSWORD=Summer2024abc"
	spans, _ := cfg.Detect(text, 0, nil)
	if got := spansOfKind(text, spans, KindSecretContext); len(got) != 0 {
		t.Fatalf("SecretContext=false 仍检出：%v", got)
	}

	if got := MaskPreview(KindSecretContext, "Summer2024abc"); got != "[secret_context]" {
		t.Fatalf("MaskPreview = %q", got)
	}
}

func TestDetectCustomRuleAndAllowlistWithUUID(t *testing.T) {
	uuid := "a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b"
	cfg := DefaultConfig()
	cfg.CustomRules = []string{uuid}
	spans, _ := cfg.Detect("id "+uuid, 0, nil)
	if got := spansOfKind("id "+uuid, spans, KindCustom); len(got) != 1 {
		t.Fatalf("custom_rules 未命中 UUID：%v", spans)
	}

	m := newTestMapping(t, 0, 0)
	cfg = DefaultConfig()
	cfg.Allowlist = []string{uuid}
	out, count, err := m.Redact("id "+uuid, 0, cfg)
	if err != nil || count != 0 || out != "id "+uuid {
		t.Fatalf("allowlist UUID 被替换：out=%q count=%d err=%v", out, count, err)
	}
}
