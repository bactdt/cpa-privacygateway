package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// kvPattern 匹配单行键值写法：KEY=value、export KEY=value、`"key": "value"`、
// `key: value`、`key = value`。第 1 组为键，第 2 组为值（可能带引号）。
// 值支持双引号内的反斜杠转义；不跨换行。
var kvPattern = regexp.MustCompile(`(?:^|[\s{,;(\[])(?:export\s+)?["']?([A-Za-z_][A-Za-z0-9_.\-]*)["']?[ \t]*[:=][ \t]*("(?:[^"\\\n]|\\.)*"|'[^'\n]*'|[^\s,;}\]'"]+)`)

// kvMatch 是一次键值扫描结果；valStart/valEnd 指向去掉外层引号后的值区间。
type kvMatch struct {
	key      string
	valStart int
	valEnd   int
}

// scanKV 找出文本中全部键值写法；结果同时供敏感上下文检测与安全键名豁免使用。
func scanKV(text string) []kvMatch {
	locs := kvPattern.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	matches := make([]kvMatch, 0, len(locs))
	for _, loc := range locs {
		keyStart, keyEnd := loc[2], loc[3]
		valStart, valEnd := loc[4], loc[5]
		if keyStart < 0 || valStart < 0 || valEnd <= valStart {
			continue
		}
		if valEnd-valStart >= 2 {
			first, last := text[valStart], text[valEnd-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				valStart++
				valEnd--
			}
		}
		if valEnd <= valStart {
			continue
		}
		matches = append(matches, kvMatch{
			key:      text[keyStart:keyEnd],
			valStart: valStart,
			valEnd:   valEnd,
		})
	}
	return matches
}

// keyWords 按 `_`、`-`、`.` 和驼峰边界切词并转大写。
// 例：dbPassword → [DB PASSWORD]；ACCESS_KEY_ID → [ACCESS KEY ID]。
func keyWords(key string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToUpper(string(current)))
			current = current[:0]
		}
	}
	var prev rune
	for i, r := range key {
		if r == '_' || r == '-' || r == '.' {
			flush()
			prev = 0
			continue
		}
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			flush()
		}
		current = append(current, r)
		prev = r
	}
	flush()
	return words
}

var sensitiveKeyWords = map[string]bool{
	"SECRET":     true,
	"TOKEN":      true,
	"PASSWORD":   true,
	"PASSWD":     true,
	"PWD":        true,
	"PRIVATE":    true,
	"CREDENTIAL": true,
	"APIKEY":     true,
}

var sensitiveKeyPairs = map[[2]string]bool{
	{"API", "KEY"}:    true,
	{"ACCESS", "KEY"}: true,
}

// IsSensitiveKey 判断键名是否含敏感词；整个键名为 PWD/OLDPWD 时除外。
func IsSensitiveKey(key string) bool {
	return isSensitiveKey(key)
}

func isSensitiveKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "PWD" || upper == "OLDPWD" {
		return false
	}
	words := keyWords(key)
	for _, word := range words {
		if sensitiveKeyWords[word] {
			return true
		}
	}
	for i := 0; i+1 < len(words); i++ {
		if sensitiveKeyPairs[[2]string{words[i], words[i+1]}] {
			return true
		}
	}
	return false
}

// sensitiveValueOK 判断敏感键名的值是否值得脱敏：≥8 rune、非纯数字、
// 非布尔词、不是完整占位符。
func sensitiveValueOK(value string) bool {
	if utf8.RuneCountInString(value) < 8 {
		return false
	}
	if isAllDigits(value) {
		return false
	}
	switch strings.ToLower(value) {
	case "true", "false", "yes", "no":
		return false
	}
	if loc := placeholderRe.FindStringIndex(value); loc != nil && loc[0] == 0 && loc[1] == len(value) {
		return false
	}
	return true
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isSafeIDKey 判断键名是否为内置安全 id 键或用户配置的安全键名；
// 敏感键名永远不安全。
func isSafeIDKey(key string, extra []string) bool {
	if isSensitiveKey(key) {
		return false
	}
	normalized := normalizeIDKey(key)
	switch normalized {
	case "processid", "pid", "traceid", "requestid":
		return true
	}
	for _, item := range extra {
		if normalizeIDKey(item) == normalized {
			return true
		}
	}
	return false
}

func normalizeIDKey(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(key)))
}

// KnownSecretPrefixes 是已知密钥前缀（小写）。safe_id_prefixes 校验用：
// 配置项不区分大小写地与其中任一项互为前缀时拒绝，避免把密钥当安全 id 放行。
var KnownSecretPrefixes = []string{
	"sk", "rk", "pk", "gh", "github_pat", "glpat", "hf", "npm",
	"xox", "akia", "asia", "aiza", "eyj",
}
