package redact

import "strings"

// RestoreJSONStats 是还原的观测统计：Restored 为已知占位符还原次数，
// Unknown 为符合格式但不在映射中的出现次数，UnknownSamples 至多 5 个（需求 4.6）。
// RestoreCounted 的输出字符串与 Restore 逐字节相同（需求 4.7）。
type RestoreJSONStats struct {
	Restored       int      `json:"restored"`
	Unknown        int      `json:"unknown"`
	UnknownSamples []string `json:"unknown_samples,omitempty"`
}

// RestoreCounted 还原文本并返回统计；与 Restore 的输出逐字节相同。
func (m *Mapping) RestoreCounted(text string) (string, RestoreJSONStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var stats RestoreJSONStats
	if len(m.phToRaw) == 0 && !strings.Contains(text, placeholderOpen) {
		return text, stats
	}
	prefix := string(placeholderPrefixFor(m.nonce))
	out := placeholderRe.ReplaceAllStringFunc(text, func(token string) string {
		if raw, ok := m.phToRaw[token]; ok {
			stats.Restored++
			return raw
		}
		if strings.HasPrefix(token, prefix) {
			stats.Unknown++
			if len(stats.UnknownSamples) < 5 {
				stats.UnknownSamples = append(stats.UnknownSamples, token)
			}
		}
		return token
	})
	return out, stats
}
