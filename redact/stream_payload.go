package redact

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// 宿主 SourceFormat 取值（见 design "Bug Details" 表）。
const (
	formatOpenAI       = "openai"
	formatGemini       = "gemini"
	formatClaude       = "claude"
	formatResponses    = "openai-response"
	formatCodex        = "codex"
	formatAntigravity  = "antigravity"
	formatInteractions = "interactions"
)

// payloadShape 是宿主流块的载荷形态。
type payloadShape int

const (
	shapeOpaque payloadShape = iota // 非 JSON、非 SSE，如 [DONE]
	shapeBare                       // 单个裸 JSON 事件
	shapeSSE                        // 单行 SSE 字段或完整 SSE 帧
)

var sseFieldPrefixes = [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")}

// payloadShapeOf 去前导空白后判定形态：以 `{`/`[` 开头且 json.Valid → bareJSON；
// 以 SSE 字段名或注释 `:` 开头 → sseText；其余为 opaque。
func payloadShapeOf(chunk []byte) payloadShape {
	trimmed := bytes.TrimLeft(chunk, " \t\r\n")
	if len(trimmed) == 0 {
		return shapeOpaque
	}
	if (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		return shapeBare
	}
	for _, prefix := range sseFieldPrefixes {
		if bytes.HasPrefix(trimmed, prefix) {
			return shapeSSE
		}
	}
	return shapeOpaque
}

// carryEnabled 判断某协议在某形态下是否启用跨事件暂扣：只有残留能在本形态
// 投递时才暂扣。openai/gemini/antigravity 注入裸 JSON；SSE 协议合成帧。
func carryEnabled(format string, shape payloadShape) bool {
	switch format {
	case formatOpenAI:
		return shape == shapeBare || shape == shapeSSE
	case formatGemini, formatAntigravity:
		return shape == shapeBare
	case formatClaude, formatResponses, formatCodex, formatInteractions:
		return shape == shapeSSE
	}
	return false
}

// isResponsesLike 判断协议是否使用 Responses 事件名结束语义（codex 与 Responses 同构）。
func isResponsesLike(format string) bool {
	return format == formatResponses || format == formatCodex
}

// RestorePayload 处理宿主投递的一个流块，从不丢弃：每个输入块恰好产出一个输出块。
// 裸 JSON 与 SSE 文本中 `data:` 行的 JSON 载荷按协议在文本字段上还原占位符，
// 跨事件被切开的占位符按文本流暂扣并在结束信号处投递；其余字节原样保留。
// changed=false 时 out 就是传入的 chunk（同一底层数组），调用方应返回空 Body
// 让宿主沿用原块。不跨块缓冲任何行。
func (s *StreamRestorer) RestorePayload(format string, chunk []byte) (out []byte, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mapping == nil || s.mapping.Size() == 0 {
		return chunk, false
	}
	s.text.stats.Chunks++
	switch payloadShapeOf(chunk) {
	case shapeBare:
		// 裸 JSON 协议的投递方式是注入字段，不产生合成帧。
		out, _ = s.restoreEvent(format, carryEnabled(format, shapeBare), "", chunk)
	case shapeSSE:
		out = s.restoreSSE(format, chunk)
	default:
		s.text.stats.Passthrough++
		return chunk, false
	}
	if bytes.Equal(out, chunk) {
		return chunk, false
	}
	return out, true
}

// restoreSSE 逐行处理本块（不跨块缓冲）：保留每行原有行尾（`\n`/`\r\n`/无），
// `data:` 行只替换 JSON 载荷字节，其他字段与注释原样；合成帧插入当前帧起点。
func (s *StreamRestorer) restoreSSE(format string, chunk []byte) []byte {
	carry := carryEnabled(format, shapeSSE)
	out := make([]byte, 0, len(chunk))
	frameStart := 0 // out 中当前帧第一行的偏移；合成帧插在这里
	eventName := ""
	for rest := chunk; len(rest) > 0; {
		full := rest
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			full = rest[:idx+1]
		}
		rest = rest[len(full):]
		line := bytes.TrimSuffix(bytes.TrimSuffix(full, []byte("\n")), []byte("\r"))
		ending := full[len(line):]
		var frames []byte
		switch {
		case len(line) == 0:
			out = append(out, full...)
			frameStart = len(out)
			eventName = ""
			continue
		case bytes.HasPrefix(line, []byte("data:")):
			var restored []byte
			restored, frames = s.restoreDataLine(format, carry, eventName, line)
			out = insertFrames(out, frameStart, frames)
			out = append(append(out, restored...), ending...)
		case bytes.HasPrefix(line, []byte("event:")):
			eventName = strings.TrimSpace(string(line[len("event:"):]))
			if carry && isResponsesLike(format) {
				frames = s.deliverByEventName(eventName)
			} else if carry && format == formatInteractions && (eventName == "done" || eventName == "interaction.completed") {
				frames = s.deliverAllFrames()
			}
			out = insertFrames(out, frameStart, frames)
			out = append(out, full...)
		default:
			out = append(out, full...)
		}
		frameStart += len(frames)
	}
	return out
}

// restoreDataLine 还原一条 `data:` 行（不含行尾）。载荷不是合法 JSON（如 [DONE]）
// 时原样返回；否则保留 `data:` 与可选的单个分隔空格，只替换载荷。
func (s *StreamRestorer) restoreDataLine(format string, carry bool, eventName string, line []byte) ([]byte, []byte) {
	head := len("data:")
	if head < len(line) && line[head] == ' ' {
		head++
	}
	payload := line[head:]
	if !json.Valid(payload) {
		s.text.stats.Passthrough++
		if format == formatInteractions && string(bytes.TrimSpace(payload)) == "[DONE]" {
			return line, s.deliverAllFrames()
		}
		return line, nil
	}
	restored, frames := s.restoreEvent(format, carry, eventName, payload)
	if bytes.Equal(restored, payload) {
		return line, frames
	}
	out := make([]byte, 0, head+len(restored))
	out = append(out, line[:head]...)
	return append(out, restored...), frames
}

// insertFrames 把合成帧插到 out 的 at 偏移处。
func insertFrames(out []byte, at int, frames []byte) []byte {
	if len(frames) == 0 {
		return out
	}
	return insertAt(out, at, frames, false)
}

// restoreEvent 还原单个合法 JSON 事件：规划 → editStrings（目标字段按文本流暂扣还原，
// 其他字符串只还原完整占位符）→ 投递本事件结束但未在本事件出现的文本流残留 →
// 为仍有暂扣的 SSE 文本流更新模板。返回事件新字节与需插在其帧之前的合成帧。
// 调用方保证 event 是合法 JSON；嵌套过深等编辑失败时原样透传。
func (s *StreamRestorer) restoreEvent(format string, carry bool, eventName string, event []byte) ([]byte, []byte) {
	t := s.text
	t.stats.Events++
	plan, planned := planEvent(format, event)
	carry = carry && planned
	targets := map[string]string{}
	if carry {
		for _, target := range plan.targets {
			targets[target.path] = target.key
		}
	}
	handled := map[string]string{} // 本事件出现的目标键 → 字段路径
	out, _, err := editStrings(event, func(path []string, value string) (string, bool) {
		joined := strings.Join(path, "\x00")
		if key, ok := targets[joined]; ok {
			handled[key] = joined
			final := plan.ends(key)
			if final && t.carry[key] != "" {
				t.stats.Flushed++
			}
			next := t.restoreText(key, value, final)
			return next, next != value
		}
		next, restored, unknown := s.mapping.RestoreCount(value)
		t.stats.Restored += restored
		t.stats.Unknown += unknown
		if !carry {
			if tail := holdbackLen(next, t.nonce); tail > 0 {
				t.stats.UnsupportedEvent++
				addDanglingSample(&t.stats, next[len(next)-tail:])
			}
		}
		return next, next != value
	})
	if err != nil {
		t.stats.Passthrough++
		return event, nil
	}
	if !carry {
		return out, nil
	}
	out, frames := s.deliverEnded(format, plan, handled, out)
	if format == formatClaude || isResponsesLike(format) || format == formatInteractions {
		s.storeTemplates(handled, eventName, out)
	}
	return out, frames
}

// deliverEnded 取出本事件结束、但本事件没有对应目标字段的文本流残留并投递：
// openai/gemini 注入到 event，claude/openai-response 返回合成帧。
func (s *StreamRestorer) deliverEnded(format string, plan eventPlan, handled map[string]string, event []byte) ([]byte, []byte) {
	t := s.text
	taken := t.takeCarry(plan.endKeys, "", plan.endAll)
	for _, prefix := range plan.endPrefixes {
		for key, value := range t.takeCarry(nil, prefix, false) {
			if taken == nil {
				taken = map[string]string{}
			}
			taken[key] = value
		}
	}
	var frames []byte
	for _, key := range sortedKeys(taken) {
		value := taken[key]
		if _, ok := handled[key]; ok {
			// 本事件出现但未结束的目标键不应被结束信号带走（防御性放回）。
			t.carry[key] = value
			continue
		}
		var ok bool
		switch {
		case format == formatOpenAI:
			event, ok = injectOpenAI(event, key, value)
		case format == formatGemini:
			event, ok = injectGemini(event, key, value)
		case format == formatAntigravity:
			event, ok = injectAntigravity(event, key, value)
		default:
			var frame []byte
			frame, ok = s.synthFrame(format, key, value)
			frames = append(frames, frame...)
		}
		s.countDelivery(ok, value)
	}
	return event, frames
}

// deliverByEventName 处理 Codex 单行模式的 `event:` 行：按事件名结束 kind
// （键最后一个 "/" 之后的段）匹配的残留，返回合成帧。
func (s *StreamRestorer) deliverByEventName(name string) []byte {
	t := s.text
	kinds, all := responsesEndByEventName(name)
	if len(t.carry) == 0 || (!all && len(kinds) == 0) {
		return nil
	}
	var keys []string
	for key := range t.carry {
		kind := key[strings.LastIndexByte(key, '/')+1:]
		for _, want := range kinds {
			if kind == want {
				keys = append(keys, key)
				break
			}
		}
	}
	taken := t.takeCarry(keys, "", all)
	var frames []byte
	for _, key := range sortedKeys(taken) {
		frame, ok := s.synthFrame(formatResponses, key, taken[key])
		frames = append(frames, frame...)
		s.countDelivery(ok, taken[key])
	}
	return frames
}

// synthFrame 用该键模板合成只携带残留的增量帧。
func (s *StreamRestorer) synthFrame(format, key, carry string) ([]byte, bool) {
	tpl, ok := s.text.templates[key]
	if !ok {
		return nil, false
	}
	var frame []byte
	switch format {
	case formatClaude:
		frame = synthClaude(tpl, carry)
	case formatInteractions:
		frame = synthInteractions(tpl, carry)
	default:
		frame = synthResponses(tpl, carry)
	}
	return frame, frame != nil
}

// synthSSEPayload 用模板路径替换为 carry 后包装成 SSE 帧；事件名由调用方解析。
func synthSSEPayload(tpl streamTemplate, carry, eventName string) []byte {
	if len(tpl.raw) == 0 || len(tpl.path) == 0 || eventName == "" {
		return nil
	}
	body, changed, err := editStrings(tpl.raw, func(path []string, _ string) (string, bool) {
		if pathEqual(path, tpl.path) {
			return carry, true
		}
		return "", false
	})
	if err != nil || !changed {
		return nil
	}
	return wrapSSEFrame(eventName, body)
}

// wrapSSEFrame 生成 "event: <name>\ndata: <json>\n\n" 帧。
func wrapSSEFrame(name string, body []byte) []byte {
	out := make([]byte, 0, len(body)+len(name)+16)
	out = append(out, "event: "+name+"\ndata: "...)
	out = append(out, body...)
	return append(out, "\n\n"...)
}

// countDelivery 记录一次残留投递：成功计入 Flushed/Synthesized，
// 失败（无模板或无法注入）时残留丢失，字节数计入 Leftover。
func (s *StreamRestorer) countDelivery(ok bool, carry string) {
	if ok {
		s.text.stats.Flushed++
		s.text.stats.Synthesized++
		return
	}
	s.text.stats.MissingTemplate++
	addDanglingSample(&s.text.stats, carry)
	s.text.stats.Leftover += len(carry)
}

// storeTemplates 为本事件出现、且处理后仍有暂扣的文本流记录模板
// （事件输出字节的副本 + 字段路径 + 当前帧 `event:` 名）。
func (s *StreamRestorer) storeTemplates(handled map[string]string, eventName string, event []byte) {
	t := s.text
	for key, path := range handled {
		if t.carry[key] == "" {
			continue
		}
		if t.templates == nil {
			t.templates = make(map[string]streamTemplate)
		}
		t.templates[key] = streamTemplate{
			raw:   append([]byte(nil), event...),
			path:  strings.Split(path, "\x00"),
			event: eventName,
		}
	}
}

// planEvent 按 SourceFormat 选择协议规划器；未知协议无计划（只做完整还原）。
func planEvent(format string, event []byte) (eventPlan, bool) {
	switch format {
	case formatOpenAI:
		return planOpenAI(event)
	case formatGemini:
		return planGemini(event)
	case formatClaude:
		return planClaude(event)
	case formatResponses, formatCodex:
		return planResponses(event)
	case formatAntigravity:
		return planAntigravity(event)
	case formatInteractions:
		return planInteractions(event)
	}
	return eventPlan{}, false
}

// ends 判断文本流 key 是否在本事件结束。
func (p eventPlan) ends(key string) bool {
	if p.endAll {
		return true
	}
	for _, k := range p.endKeys {
		if k == key {
			return true
		}
	}
	for _, prefix := range p.endPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
