package redact

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

type Kind string

const (
	KindCredential    Kind = "credential"
	KindEntropy       Kind = "entropy"
	KindEmail         Kind = "email"
	KindPhone         Kind = "phone"
	KindIDCard        Kind = "id_card"
	KindBankCard      Kind = "bank_card"
	KindCustom        Kind = "custom"
	KindSecretContext Kind = "secret_context"
)

type Config struct {
	Credentials    bool
	Entropy        bool
	Email          bool
	Phone          bool
	IDCard         bool
	BankCard       bool
	MinEntropy     float64
	MinEntropyLen  int
	Allowlist      []string
	MaxFindings    int
	CustomRules    []string
	SecretContext  bool
	SafeIDKeys     []string
	SafeIDPrefixes []string
}

func DefaultConfig() Config {
	return Config{
		Credentials:   true,
		Entropy:       true,
		Email:         true,
		Phone:         true,
		IDCard:        true,
		BankCard:      true,
		MinEntropy:    3.5,
		MinEntropyLen: 24,
		MaxFindings:   2000,
		SecretContext: true,
	}
}

type span struct {
	start int
	end   int
	kind  Kind
	allow bool
}

type match struct {
	start int
	end   int
	kind  Kind
}

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,24}`)
	phonePattern = regexp.MustCompile(`(?:(?:\+|00)86[\s\-]?)?1[3-9]\d{9}`)
	tokenPattern = regexp.MustCompile(`\b[A-Za-z0-9_\-]{16,}\b`)
	digitPattern = regexp.MustCompile(`\d`)
)

type credentialRule struct {
	kind    Kind
	pattern *regexp.Regexp
}

var credentialRules = []credentialRule{
	{KindCredential, regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)},
	{KindCredential, regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{KindCredential, regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{KindCredential, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{KindCredential, regexp.MustCompile(`ASIA[0-9A-Z]{16}`)},
	{KindCredential, regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{KindCredential, regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`)},
	{KindCredential, regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
	{KindCredential, regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
}

func (c Config) Detect(text string, base int, out []span) ([]span, bool) {
	if text == "" {
		return out, false
	}
	allowed := make([]string, 0, len(c.Allowlist))
	for _, value := range c.Allowlist {
		if value != "" {
			allowed = append(allowed, value)
		}
	}
	if c.Credentials {
		for _, rule := range credentialRules {
			for _, loc := range rule.pattern.FindAllStringIndex(text, -1) {
				out = append(out, span{start: base + loc[0], end: base + loc[1], kind: rule.kind})
			}
		}
	}
	for _, pattern := range c.CustomRules {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		for _, loc := range re.FindAllStringIndex(text, -1) {
			if loc[0] != loc[1] {
				out = append(out, span{start: base + loc[0], end: base + loc[1], kind: KindCustom})
			}
		}
	}
	if c.Email {
		for _, loc := range emailPattern.FindAllStringIndex(text, -1) {
			out = append(out, span{start: base + loc[0], end: base + loc[1], kind: KindEmail})
		}
	}
	if c.Phone {
		for _, loc := range phonePattern.FindAllStringIndex(text, -1) {
			if !phoneAtBoundary(text, loc[0], loc[1]) {
				continue
			}
			out = append(out, span{start: base + loc[0], end: base + loc[1], kind: KindPhone})
		}
	}
	if c.IDCard {
		for _, candidate := range idCardCandidates(text) {
			out = append(out, span{start: base + candidate[0], end: base + candidate[1], kind: KindIDCard})
		}
	}
	if c.BankCard {
		for _, candidate := range bankCardCandidates(text) {
			out = append(out, span{start: base + candidate[0], end: base + candidate[1], kind: KindBankCard})
		}
	}
	var kvs []kvMatch
	if c.SecretContext || c.Entropy {
		kvs = scanKV(text)
	}
	if c.SecretContext {
		for _, kv := range kvs {
			if isSensitiveKey(kv.key) && sensitiveValueOK(text[kv.valStart:kv.valEnd]) {
				out = append(out, span{start: base + kv.valStart, end: base + kv.valEnd, kind: KindSecretContext})
			}
		}
	}
	if c.Entropy {
		exempt := exemptRanges(text, kvs, c)
		out = appendEntropy(text, base, c, exempt, out)
	}
	for _, value := range allowed {
		offset := 0
		for {
			index := strings.Index(text[offset:], value)
			if index < 0 {
				break
			}
			start := offset + index
			out = append(out, span{base + start, base + start + len(value), "", true})
			offset = start + len(value)
		}
	}
	return c.capFindings(out)
}

// capFindings 把单字段检出数量限制到 MaxFindings；allowlist 条目不计入上限。
// 超限不静默丢弃（丢弃等于漏扫放行），而是返回 truncated，由 Redact 拒绝或放行。
func (c Config) capFindings(out []span) ([]span, bool) {
	detected := 0
	for _, s := range out {
		if !s.allow {
			detected++
		}
	}
	if c.MaxFindings <= 0 || detected <= c.MaxFindings {
		return out, false
	}
	kept := make([]span, 0, c.MaxFindings+len(out)-detected)
	count := 0
	for _, s := range out {
		if s.allow {
			kept = append(kept, s)
			continue
		}
		if count >= c.MaxFindings {
			continue
		}
		count++
		kept = append(kept, s)
	}
	return kept, true
}

func appendEntropy(text string, base int, c Config, exempt [][2]int, out []span) []span {
	minLen := c.MinEntropyLen
	if minLen <= 0 {
		minLen = 24
	}
	threshold := c.MinEntropy
	if threshold <= 0 {
		threshold = 3.5
	}
	for _, loc := range tokenPattern.FindAllStringIndex(text, -1) {
		candidate := text[loc[0]:loc[1]]
		if len(candidate) < minLen {
			continue
		}
		if !digitPattern.MatchString(candidate) {
			continue
		}
		if shannon(candidate) < threshold {
			continue
		}
		if fullyExempt(loc[0], loc[1], exempt) {
			continue
		}
		out = append(out, span{start: base + loc[0], end: base + loc[1], kind: KindEntropy})
	}
	return out
}

func shannon(value string) float64 {
	if value == "" {
		return 0
	}
	counts := make(map[rune]int)
	total := 0
	for _, r := range value {
		counts[r]++
		total++
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// phoneAtBoundary 要求手机号候选前后都不是数字或字母，
// 排除毫秒时间戳、订单号等长数字串被截成 11 位误报。
func phoneAtBoundary(text string, start, end int) bool {
	if start > 0 {
		prev := text[start-1]
		if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') {
			return false
		}
	}
	if end < len(text) {
		next := text[end]
		if (next >= '0' && next <= '9') || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') {
			return false
		}
	}
	return true
}

func idCardCandidates(text string) [][2]int {
	var found [][2]int
	length := len(text)
	for i := 0; i < length; i++ {
		if text[i] < '0' || text[i] > '9' {
			continue
		}
		if i > 0 && text[i-1] >= '0' && text[i-1] <= '9' {
			continue
		}
		end := i
		for end < length && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		// 17 位数字 + 可选 X/x 校验位（TD-CPA-028）。
		if end-i == 17 && end < length && (text[end] == 'X' || text[end] == 'x') {
			if nextIsWordByte(text, end+1) {
				continue
			}
			if validIDCard(text[i : end+1]) {
				found = append(found, [2]int{i, end + 1})
			}
			continue
		}
		if end-i == 18 && validIDCard(text[i:end]) {
			found = append(found, [2]int{i, end})
		}
		i = end - 1
	}
	return found
}

// nextIsWordByte 判断偏移处的字节是否为字母或数字，用于边界检查。
func nextIsWordByte(text string, offset int) bool {
	if offset >= len(text) {
		return false
	}
	ch := text[offset]
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

var idCardWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}

var idCardChecks = [11]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}

func validIDCard(value string) bool {
	if len(value) != 18 {
		return false
	}
	for i := 0; i < 17; i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	last := value[17]
	if last == 'x' {
		last = 'X'
	}
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(value[i]-'0') * idCardWeights[i]
	}
	return idCardChecks[sum%11] == last
}

func bankCardCandidates(text string) [][2]int {
	var found [][2]int
	length := len(text)
	for i := 0; i < length; i++ {
		digit := text[i]
		if digit < '0' || digit > '9' {
			continue
		}
		// 保留数字起始约束：候选必须从数字开始；
		// 前导空格/连字符不再导致跳过（TD-CPA-029）。
		end := i
		separators := 0
		for end < length {
			ch := text[end]
			if ch >= '0' && ch <= '9' {
				end++
				continue
			}
			if (ch == ' ' || ch == '-') && end+1 < length && text[end+1] >= '0' && text[end+1] <= '9' {
				separators++
				end++
				continue
			}
			break
		}
		raw := text[i:end]
		digits := normalizeDigits(raw)
		if len(digits) >= 13 && len(digits) <= 19 && separators <= 5 && luhn(digits) {
			found = append(found, [2]int{i, end})
		}
		i = end - 1
	}
	return found
}

func normalizeDigits(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] >= '0' && value[i] <= '9' {
			builder.WriteByte(value[i])
		}
	}
	return builder.String()
}

func luhn(digits string) bool {
	if len(digits) == 0 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		value := int(digits[i] - '0')
		if double {
			value *= 2
			if value > 9 {
				value -= 9
			}
		}
		sum += value
		double = !double
	}
	return sum%10 == 0
}

func resolveSpans(spans []span) []span {
	if len(spans) == 0 {
		return nil
	}
	sorted := make([]span, len(spans))
	copy(sorted, spans)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end > sorted[j].end
	})
	var allowedSpans []span
	detected := make([]span, 0, len(sorted))
	for _, candidate := range sorted {
		if candidate.allow {
			allowedSpans = append(allowedSpans, candidate)
			continue
		}
		if candidate.end <= candidate.start {
			continue
		}
		if overlaps(detected, candidate) {
			continue
		}
		detected = append(detected, candidate)
	}
	result := make([]span, 0, len(detected))
	for _, candidate := range detected {
		if overlaps(allowedSpans, candidate) {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func overlaps(existing []span, candidate span) bool {
	for _, item := range existing {
		if candidate.start < item.end && item.start < candidate.end {
			return true
		}
	}
	return false
}
