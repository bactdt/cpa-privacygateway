package redact

import "strings"

// StreamStats 汇总一个 StreamRestorer 生命周期内的流式还原统计，
// 供 request-trace-log 暴露；所有计数只增不减（reset 不清零）。
type StreamStats struct {
	Chunks      int // RestorePayload 调用次数
	Events      int // 成功解析的 JSON 事件数
	Restored    int // 还原的占位符个数
	Unknown     int // 本 nonce 但不在映射中的占位符个数
	Carried     int // 发生暂扣的次数
	MaxPending  int // 单个文本流最大暂扣字节数
	Flushed     int // 结束信号投递残留的次数
	Synthesized int // 注入字段或合成增量帧的次数
	Passthrough int // 非 JSON / 解析失败原样透传的载荷数
	Leftover    int // Flush 时未能投递而丢弃的残留字节数
}

// streamTemplate 记录 SSE 协议某文本流最近一次 delta 事件，
// 用于在结束信号前合成只携带残留文本的增量帧。
type streamTemplate struct {
	raw   []byte   // 最近一次 delta 事件的 JSON 原字节
	path  []string // 文本字段在 raw 中的路径段
	event string   // SSE `event:` 名，可能为空
}

// textCarry 是按文本流键暂扣占位符前缀的残留引擎。
// 不自带锁：调用方（StreamRestorer）必须持有 StreamRestorer.mu。
type textCarry struct {
	mapping   *Mapping
	nonce     string
	carry     map[string]string
	templates map[string]streamTemplate
	stats     StreamStats
}

func newTextCarry(mapping *Mapping) *textCarry {
	tc := &textCarry{mapping: mapping}
	if mapping != nil {
		tc.nonce = mapping.Nonce()
	}
	return tc
}

// restoreText 把 key 流上次的残留与 value 拼接后完全还原。final 为真时清空残留
// 并返回全部文本；否则把末尾可能是本 nonce 占位符开头的部分暂扣，返回其余前缀。
// 暂扣部分总以 ASCII '[' 开头，切分不会破坏 UTF-8。
func (t *textCarry) restoreText(key, value string, final bool) string {
	combined := t.carry[key] + value
	out := combined
	if t.mapping != nil {
		var restored, unknown int
		out, restored, unknown = t.mapping.RestoreCount(combined)
		t.stats.Restored += restored
		t.stats.Unknown += unknown
	}
	if final {
		delete(t.carry, key)
		return out
	}
	tail := holdbackLen(out, t.nonce)
	if tail == 0 {
		delete(t.carry, key)
		return out
	}
	if t.carry == nil {
		t.carry = make(map[string]string)
	}
	t.carry[key] = out[len(out)-tail:]
	t.stats.Carried++
	if tail > t.stats.MaxPending {
		t.stats.MaxPending = tail
	}
	return out[:len(out)-tail]
}

// takeCarry 取出并清空残留：all 为真取全部；否则取 keys 中的键与以 prefix
// 开头的键（prefix 为空时不按前缀匹配）。空残留不出现在结果中。
// Flushed 计数由实际完成投递的调用方累加。
func (t *textCarry) takeCarry(keys []string, prefix string, all bool) map[string]string {
	if len(t.carry) == 0 {
		return nil
	}
	taken := make(map[string]string)
	take := func(key string) {
		if value, ok := t.carry[key]; ok {
			delete(t.carry, key)
			if value != "" {
				taken[key] = value
			}
		}
	}
	for key := range t.carry {
		if all || (prefix != "" && strings.HasPrefix(key, prefix)) {
			take(key)
		}
	}
	for _, key := range keys {
		take(key)
	}
	if len(taken) == 0 {
		return nil
	}
	return taken
}

// pendingBytes 返回所有文本流暂扣的字节总数。
func (t *textCarry) pendingBytes() int {
	total := 0
	for _, value := range t.carry {
		total += len(value)
	}
	return total
}

// reset 清空残留与模板；stats 保留，作为整个请求的累计统计。
func (t *textCarry) reset() {
	t.carry = nil
	t.templates = nil
}

// holdbackLen 返回 text 末尾最长的、可能是 `[[PG_<nonce>_<digits>]]` 开头的
// 后缀长度（≤ maxPlaceholderLen）。已闭合的完整占位符不会再变化，不暂扣。
func holdbackLen(text, nonce string) int {
	if nonce == "" || text == "" {
		return 0
	}
	start := len(text) - maxPlaceholderLen
	if start < 0 {
		start = 0
	}
	for i := start; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		suffix := text[i:]
		if strings.HasSuffix(suffix, placeholderClose) && placeholderRe.MatchString(suffix) {
			continue
		}
		if isPlaceholderPrefix([]byte(suffix), nonce) {
			return len(text) - i
		}
	}
	return 0
}
