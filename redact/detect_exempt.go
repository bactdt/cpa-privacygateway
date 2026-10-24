package redact

import (
	"regexp"
	"strings"
)

// uuidPattern 匹配标准 8-4-4-4-12 UUID；整段豁免高熵检测。
var uuidPattern = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

// commonFileExtensions 是文件名主体豁免支持的扩展名（不区分大小写）。
// 长的写在前面，便于匹配 .tar.gz 这类复合扩展名。
var commonFileExtensions = []string{
	"tar.gz", "tar.bz2", "tar.xz",
	"sqlite", "tar", "tgz", "zip", "gz", "7z", "log", "txt", "json",
	"yaml", "yml", "csv", "sql", "db", "bak", "md", "html", "png", "jpg", "pdf",
}

// exemptRanges 汇总本字段中可安全放过的高熵区间：UUID、常见扩展名文件名的
// 主体、安全键名的值、配置的安全 id 前缀。豁免只用于过滤高熵候选。
func exemptRanges(text string, kvs []kvMatch, c Config) [][2]int {
	var ranges [][2]int
	for _, loc := range uuidPattern.FindAllStringIndex(text, -1) {
		ranges = append(ranges, [2]int{loc[0], loc[1]})
	}
	for _, loc := range tokenPattern.FindAllStringIndex(text, -1) {
		if hasFileExtensionAt(text, loc[1]) {
			ranges = append(ranges, [2]int{loc[0], loc[1]})
			continue
		}
		candidate := text[loc[0]:loc[1]]
		for _, prefix := range c.SafeIDPrefixes {
			if prefix != "" && strings.HasPrefix(candidate, prefix) {
				ranges = append(ranges, [2]int{loc[0], loc[1]})
				break
			}
		}
	}
	for _, kv := range kvs {
		if isSafeIDKey(kv.key, c.SafeIDKeys) {
			ranges = append(ranges, [2]int{kv.valStart, kv.valEnd})
		}
	}
	return ranges
}

// hasFileExtensionAt 判断 offset 处是否紧跟 `.` + 常见扩展名，且扩展名后是
// 文本结尾或非 [A-Za-z0-9_-] 字符。
func hasFileExtensionAt(text string, offset int) bool {
	if offset >= len(text) || text[offset] != '.' {
		return false
	}
	rest := text[offset+1:]
	lower := strings.ToLower(rest)
	for _, ext := range commonFileExtensions {
		if len(lower) < len(ext) || lower[:len(ext)] != ext {
			continue
		}
		if len(rest) == len(ext) || !isTokenByte(rest[len(ext)]) {
			return true
		}
	}
	return false
}

func isTokenByte(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '-'
}

// fullyExempt 判断候选区间是否完全落在某个豁免区间内。
func fullyExempt(start, end int, ranges [][2]int) bool {
	for _, r := range ranges {
		if start >= r[0] && end <= r[1] {
			return true
		}
	}
	return false
}
