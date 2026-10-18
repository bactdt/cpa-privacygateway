package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaskPreviewEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user@example.com", "u***@example.com"},
		{"a@b.cc", "*@b.cc"},
		{"user@example." + strings.Repeat("x", 40), "u***@example." + strings.Repeat("x", 23) + "…"},
		{"notanemail", "[email]"},
		{"@onlydomain.com", "[email]"},
	}
	for _, c := range cases {
		if got := MaskPreview(KindEmail, c.in); got != c.want {
			t.Errorf("MaskPreview(email, %q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPreviewPhone(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+8613812345678", "138****5678"},
		{"008613812345678", "138****5678"},
		{"13812345678", "138****5678"},
		{"123", "[phone]"},
	}
	for _, c := range cases {
		if got := MaskPreview(KindPhone, c.in); got != c.want {
			t.Errorf("MaskPreview(phone, %q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPreviewIDCard(t *testing.T) {
	if got := MaskPreview(KindIDCard, "110101199003071239"); got != "1101**********1239" {
		t.Errorf("18 位: %q", got)
	}
	if got := MaskPreview(KindIDCard, "110101900307123"); got != "1101*******7123" {
		t.Errorf("15 位: %q", got)
	}
	if got := MaskPreview(KindIDCard, "110101 19900307 1239"); got != "1101**********1239" {
		t.Errorf("带空白: %q", got)
	}
	if got := MaskPreview(KindIDCard, "12345"); got != "[id_card]" {
		t.Errorf("长度不符: %q", got)
	}
}

func TestMaskPreviewBankCard(t *testing.T) {
	if got := MaskPreview(KindBankCard, "6217 0000 1234 7893"); got != "6217********7893" {
		t.Errorf("带空格: %q", got)
	}
	if got := MaskPreview(KindBankCard, "123"); got != "[bank_card]" {
		t.Errorf("太短: %q", got)
	}
}

func TestMaskPreviewCredential(t *testing.T) {
	cases := []struct{ in, want string }{
		{"-----BEGIN RSA PRIVATE KEY-----xxxx-----END RSA PRIVATE KEY-----", "[private_key]"},
		{"sk-tEMPabcdefghijklmnopABCD", "sk-t****ABCD"},
		{"ghp_abcdefghijklmnopqrstuvwx", "ghp_a****uvwx"},
		{"sk-short", "sk-****"},
		{"noprefix-short", "[credential]"},
		{"AKIAIOSFODNN7EXAMPLE", "AKIAI****MPLE"},
	}
	for _, c := range cases {
		if got := MaskPreview(KindCredential, c.in); got != c.want {
			t.Errorf("MaskPreview(credential, %q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPreviewEntropy(t *testing.T) {
	if got := MaskPreview(KindEntropy, "abcdefghijklmnop"); got != "ab****op" {
		t.Errorf("≥16: %q", got)
	}
	if got := MaskPreview(KindEntropy, "short"); got != "[entropy]" {
		t.Errorf("<16: %q", got)
	}
}

func TestMaskPreviewUnknownKind(t *testing.T) {
	if got := MaskPreview(Kind("custom_x"), "whatever"); got != "[custom_x]" {
		t.Errorf("合法自定义: %q", got)
	}
	if got := MaskPreview(Kind("Bad-Kind!"), "whatever"); got != "[unknown]" {
		t.Errorf("非法 Kind: %q", got)
	}
}

func TestMaskPreviewNeverContainsRaw(t *testing.T) {
	raws := map[Kind]string{
		KindEmail:      "alice.smith@very-long-domain-name.example.com",
		KindPhone:      "+8613912345678",
		KindIDCard:     "110101199003071239",
		KindBankCard:   "62170000123456789",
		KindCredential: "sk-supersecretkey0123456789abcdef",
		KindEntropy:    "xK9#mP2$vL8nQ4wR7tY3",
	}
	for kind, raw := range raws {
		out := MaskPreview(kind, raw)
		if out == raw || strings.Contains(out, raw) {
			t.Errorf("%s: 预览 %q 泄露原值", kind, out)
		}
		if len([]rune(out)) > maskPreviewMaxRunes {
			t.Errorf("%s: 预览超 48 rune: %q", kind, out)
		}
		if !utf8.ValidString(out) {
			t.Errorf("%s: 预览非法 UTF-8: %q", kind, out)
		}
	}
}

func TestMaskPreviewSanitizesControlChars(t *testing.T) {
	out := MaskPreview(KindEmail, "a\nb@example.com")
	if strings.ContainsAny(out, "\n\r\t") {
		t.Errorf("控制字符未清洗: %q", out)
	}
	// sanitize 发生在掩码前：本地部分首 rune 是 a，控制符不影响输出结构；
	// 用域名含控制符的用例验证 ? 可见。
	out2 := MaskPreview(KindEmail, "ab@exa\nmple.com")
	if !strings.Contains(out2, "?") {
		t.Errorf("域名控制字符应替换为 ?: %q", out2)
	}
	if strings.ContainsAny(out2, "\n\r\t") {
		t.Errorf("输出残留控制字符: %q", out2)
	}
}

func TestRenderPath(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"messages", "0", "content"}, "messages.[0].content"},
		{[]string{"messages", "1", "custom_user_key"}, "messages.[1].*"},
		{[]string{"tools", "0", "function", "arguments"}, "tools.[0].function.arguments"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := RenderPath(c.in); got != c.want {
			t.Errorf("RenderPath(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderPathLongPath(t *testing.T) {
	path := make([]string, 12)
	for i := range path {
		path[i] = "secret_key_x"
	}
	got := RenderPath(path)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("长路径应以 … 开头: %q", got)
	}
	segs := strings.Split(strings.TrimPrefix(got, "…"), ".")
	if len(segs) > 9 { // 8 段内容
		t.Errorf("长路径段数超限: %q", got)
	}
	if strings.Contains(got, "secret_key_x") {
		t.Errorf("非白名单键不应出现: %q", got)
	}
	if len(got) > 96 {
		t.Errorf("输出超 96 字节: %d", len(got))
	}
}

func TestRenderPathWhitelistCompleteness(t *testing.T) {
	for _, k := range []string{"text", "content", "prompt", "type", "role", "model", "messages", "contents", "parts", "tools", "tool_calls", "function", "functionCall", "input", "system", "arguments", "args"} {
		if !renderPathWhitelist[k] {
			t.Errorf("白名单缺少 %q", k)
		}
	}
}
