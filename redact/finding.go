package redact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Finding 是一次检出的可观测描述，供追踪日志消费。
// 绝不包含敏感原值：Preview 由 MaskPreview 生成，Path 由 RenderPath 白名单化。
type Finding struct {
	Kind        Kind   `json:"kind"`
	Placeholder string `json:"placeholder"`
	Preview     string `json:"preview"`
	Path        string `json:"path"`
	Reused      bool   `json:"reused"`
}

// FindingSink 是检出观测回调。为 nil 时 RedactJSON 输出与未接线时逐字节相同；
// 实现侧 panic 由 redact 包恢复，不影响脱敏结果。
type FindingSink func(Finding)

// maskPreviewMaxRunes 是掩码预览的输出上限（需求 2.2）。
const maskPreviewMaxRunes = 48

// credentialPublicPrefixes 是已知公开前缀（需求 1.8）。
var credentialPublicPrefixes = []string{
	"sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
	"AKIA", "ASIA", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "eyJ",
}

var validKindName = regexp.MustCompile(`^[a-z_]{1,16}$`)

// MaskPreview 生成敏感值的掩码预览：任何输入下都不泄露完整原值，
// 输出不超过 48 rune、可见原值 rune 数不超过该 Kind 的上限（需求 1、2）。
func MaskPreview(kind Kind, raw string) string {
	clean := sanitizeRunes(raw)
	var out string
	switch kind {
	case KindEmail:
		out = maskEmail(clean)
	case KindPhone:
		out = maskPhone(clean)
	case KindIDCard:
		out = maskIDCard(clean)
	case KindBankCard:
		out = maskBankCard(clean)
	case KindCredential:
		out = maskCredential(clean)
	case KindEntropy:
		out = maskEntropy(clean)
	case KindCustom:
		out = "[" + string(KindCustom) + "]"
	default:
		if validKindName.MatchString(string(kind)) {
			out = "[" + string(kind) + "]"
		} else {
			out = "[unknown]"
		}
	}
	return truncateRunes(out, maskPreviewMaxRunes)
}

// sanitizeRunes 把无效 UTF-8 字节、控制字符与换行替换为 ?（需求 2.1）。
func sanitizeRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == utf8.RuneError || (r < 0x20 && r != 0) || r == 0x7f {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncateRunes 截断到 cap 个 rune，超出时以 … 结尾（需求 2.2）。
func truncateRunes(s string, cap int) string {
	rs := []rune(s)
	if len(rs) <= cap {
		return s
	}
	return string(rs[:cap-1]) + "…"
}

// maskEmail：本地部分首 rune + ***@域名；本地部分 1 rune 时 *@域名；
// 域名超 32 rune 取前 31 + …（需求 1.1–1.3）。
func maskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return "[" + string(KindEmail) + "]"
	}
	local := []rune(s[:at])
	domain := []rune(s[at+1:])
	if len(domain) > 32 {
		domain = append(domain[:31], '…')
	}
	if len(local) == 1 {
		return "*@" + string(domain)
	}
	return string(local[0]) + "***@" + string(domain)
}

// maskPhone：取最后 11 位数字，d[0:3] + **** + d[7:11]（需求 1.4）。
func maskPhone(s string) string {
	digits := digitsOnly(s)
	if len(digits) < 11 {
		return "[" + string(KindPhone) + "]"
	}
	d := digits[len(digits)-11:]
	return d[0:3] + "****" + d[7:11]
}

// maskIDCard：去空白后 15/18 位，前 4 + (len-8) 个 * + 后 4（需求 1.5，
// 唯一长度可变的掩码段）。
func maskIDCard(s string) string {
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if len(compact) != 15 && len(compact) != 18 {
		return "[" + string(KindIDCard) + "]"
	}
	return compact[:4] + strings.Repeat("*", len(compact)-8) + compact[len(compact)-4:]
}

// maskBankCard：≥12 位数字，前 4 + ******** + 后 4（需求 1.6）。
func maskBankCard(s string) string {
	digits := digitsOnly(s)
	if len(digits) < 12 {
		return "[" + string(KindBankCard) + "]"
	}
	return digits[:4] + "********" + digits[len(digits)-4:]
}

// maskCredential：私钥 → [private_key]；已知前缀且 body≥16 rune → 前缀+首 1+****+末 4；
// body<16 rune → 前缀+**** 或 [credential]（需求 1.7–1.9）。
func maskCredential(s string) string {
	if strings.Contains(s, "PRIVATE KEY") {
		return "[private_key]"
	}
	prefix, body := "", s
	for _, p := range credentialPublicPrefixes {
		if strings.HasPrefix(s, p) {
			prefix, body = p, s[len(p):]
			break
		}
	}
	brs := []rune(body)
	if len(brs) >= 16 {
		return prefix + string(brs[0]) + "****" + string(brs[len(brs)-4:])
	}
	if prefix != "" {
		return prefix + "****"
	}
	return "[" + string(KindCredential) + "]"
}

// maskEntropy：≥16 rune → 首 2 + **** + 末 2（需求 1.10）。
func maskEntropy(s string) string {
	rs := []rune(s)
	if len(rs) < 16 {
		return "[" + string(KindEntropy) + "]"
	}
	return string(rs[:2]) + "****" + string(rs[len(rs)-2:])
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// renderPathWhitelist 是路径白名单：协议层面的已知键原样输出，
// 其余键输出为 *（需求 3.2；textKeys/structuralKeys 见 walk.go）。
var renderPathWhitelist = func() map[string]bool {
	w := map[string]bool{}
	for k := range textKeys {
		w[k] = true
	}
	for k := range structuralKeys {
		w[k] = true
	}
	for _, format := range []string{"openai", "claude", "gemini"} {
		for k := range toolArgKeys(format) {
			w[k] = true
		}
	}
	for _, k := range []string{"messages", "contents", "parts", "tools", "tool_calls", "function", "functionCall", "input", "system"} {
		w[k] = true
	}
	return w
}()

// RenderPath 把 JSON 遍历路径渲染为白名单形式：数组下标 [n]、白名单键原样、
// 其余键为 *；超过 8 段只保留最后 8 段并以 … 开头（需求 3）。
func RenderPath(path []string) string {
	const maxSegs = 8
	var b strings.Builder
	if len(path) > maxSegs {
		b.WriteString("…")
		path = path[len(path)-maxSegs:]
	}
	for i, seg := range path {
		if i > 0 {
			b.WriteByte('.')
		}
		if isArrayIndex(seg) {
			b.WriteByte('[')
			b.WriteString(seg)
			b.WriteByte(']')
		} else if renderPathWhitelist[seg] {
			b.WriteString(seg)
		} else {
			b.WriteByte('*')
		}
	}
	out := b.String()
	if len(out) > 96 {
		out = out[:96]
	}
	return out
}

// isArrayIndex 判断路径段是否为数组下标（walkValue 以纯数字形式入路径）。
func isArrayIndex(seg string) bool {
	if len(seg) == 0 || len(seg) > 9 {
		return false
	}
	for i := 0; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return false
		}
	}
	return true
}
