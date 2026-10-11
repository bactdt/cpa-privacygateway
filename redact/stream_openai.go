package redact

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// textTarget 是事件内一个需要按文本流暂扣还原的字符串字段。
// path 为 transformJSON/editStrings 风格的路径段以 "\x00" 连接（数组下标为十进制位置）；
// key 为跨事件稳定的文本流键（如 OpenAI 的 "c0/content"）。
type textTarget struct {
	path string
	key  string
}

// eventPlan 是协议规划器对单个事件的解读，供 Claude/Gemini/Responses 规划器复用。
//   - targets：本事件携带的文本流字段（仅包含实际以字符串出现的字段）。
//   - endKeys：本事件结束的具体文本流键。
//   - endPrefixes：本事件结束的键前缀，匹配同前缀的全部键（如 "c0/" 表示 choice 0 的全部流）。
//   - endAll：本事件结束全部文本流。
type eventPlan struct {
	targets     []textTarget
	endKeys     []string
	endPrefixes []string
	endAll      bool
}

// joinPath 把路径段连接为 textTarget.path 形式。
func joinPath(segments ...string) string {
	return strings.Join(segments, "\x00")
}

// openAITextFields 是 choices[i].delta 下按文本流处理的字符串字段。
var openAITextFields = []string{"content", "reasoning_content", "reasoning", "refusal"}

type openAIChunk struct {
	Choices []json.RawMessage `json:"choices"`
}

type openAIChoice struct {
	Index        json.RawMessage `json:"index"`
	Delta        json.RawMessage `json:"delta"`
	FinishReason json.RawMessage `json:"finish_reason"`
}

type openAIToolCall struct {
	Index    json.RawMessage `json:"index"`
	Function json.RawMessage `json:"function"`
}

type openAIFunction struct {
	Arguments json.RawMessage `json:"arguments"`
}

// planOpenAI 解读 OpenAI chat completions 的裸 JSON 事件。
// 键使用 choices[i].index 与 tool_calls[j].index（缺失或非整数时退回数组位置）：
// 文本字段 "c<index>/<field>"，工具参数 "c<index>/tool<tool.index>"。
// finish_reason 非 null 且非空 → endPrefixes 追加 "c<index>/"。
// 非对象或没有 choices 数组（含 choices:[]）时 ok 为 false。
func planOpenAI(event []byte) (eventPlan, bool) {
	var chunk openAIChunk
	if err := json.Unmarshal(event, &chunk); err != nil || len(chunk.Choices) == 0 {
		return eventPlan{}, false
	}
	var plan eventPlan
	for pos, rawChoice := range chunk.Choices {
		var choice openAIChoice
		if err := json.Unmarshal(rawChoice, &choice); err != nil {
			continue
		}
		prefix := "c" + strconv.Itoa(indexOr(choice.Index, pos)) + "/"
		base := []string{"choices", strconv.Itoa(pos), "delta"}
		plan.targets = append(plan.targets, openAIDeltaTargets(choice.Delta, base, prefix)...)
		if finishSet(choice.FinishReason) {
			plan.endPrefixes = append(plan.endPrefixes, prefix)
		}
	}
	return plan, true
}

func openAIDeltaTargets(rawDelta json.RawMessage, base []string, prefix string) []textTarget {
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(rawDelta, &delta); err != nil || delta == nil {
		return nil
	}
	var targets []textTarget
	for _, field := range openAITextFields {
		if isJSONString(delta[field]) {
			targets = append(targets, textTarget{path: joinPath(append(base, field)...), key: prefix + field})
		}
	}
	var calls []json.RawMessage
	if err := json.Unmarshal(delta["tool_calls"], &calls); err != nil {
		return targets
	}
	for j, rawCall := range calls {
		var call openAIToolCall
		var fn openAIFunction
		if json.Unmarshal(rawCall, &call) != nil || json.Unmarshal(call.Function, &fn) != nil || !isJSONString(fn.Arguments) {
			continue
		}
		path := joinPath(append(base, "tool_calls", strconv.Itoa(j), "function", "arguments")...)
		targets = append(targets, textTarget{path: path, key: prefix + "tool" + strconv.Itoa(indexOr(call.Index, j))})
	}
	return targets
}

// injectOpenAI 把残留 carry 作为新字段注入结束事件中 key 对应的 choice。
// 仅用于该字段在事件中缺失的情况（已存在时由调用方直接前置，这里返回 false 以免重复键）：
// 文本字段插入 choices[i].delta；工具参数在已有 delta.tool_calls 开头插入
// {"index":k,"function":{"arguments":carry}}，否则插入整个 tool_calls 数组；
// choice 缺 delta 时插入 "delta":{...}。无法定位时返回 (event, false)。
func injectOpenAI(event []byte, key, carry string) ([]byte, bool) {
	choiceIndex, field, ok := parseOpenAIKey(key)
	if !ok {
		return event, false
	}
	pos, delta, ok := findOpenAIChoice(event, choiceIndex)
	if !ok {
		return event, false
	}
	choicePath := []string{"choices", strconv.Itoa(pos)}
	deltaPath := append(append([]string{}, choicePath...), "delta")
	literal := encodeTextLiteral(carry)

	var member []byte // 要插入 delta 对象的成员
	if strings.HasPrefix(field, "tool") {
		toolIndex := strings.TrimPrefix(field, "tool")
		element := []byte(`{"index":` + toolIndex + `,"function":{"arguments":` + string(literal) + `}}`)
		if raw, present := delta["tool_calls"]; present {
			if len(raw) == 0 || raw[0] != '[' {
				return event, false
			}
			return insertInto(event, append(deltaPath, "tool_calls"), element)
		}
		member = []byte(`"tool_calls":[` + string(element) + `]`)
	} else {
		if _, present := delta[field]; present {
			return event, false
		}
		member = append([]byte(`"`+field+`":`), literal...)
	}
	if delta == nil {
		return insertInto(event, choicePath, []byte(`"delta":{`+string(member)+`}`))
	}
	return insertInto(event, deltaPath, member)
}

// parseOpenAIKey 解析 "c<index>/<field>"，field 为文本字段名或 "tool<k>"。
func parseOpenAIKey(key string) (int, string, bool) {
	rest, ok := strings.CutPrefix(key, "c")
	if !ok {
		return 0, "", false
	}
	num, field, ok := strings.Cut(rest, "/")
	index, err := strconv.Atoi(num)
	if !ok || err != nil {
		return 0, "", false
	}
	if toolIndex, isTool := strings.CutPrefix(field, "tool"); isTool {
		if _, err := strconv.Atoi(toolIndex); err != nil {
			return 0, "", false
		}
		return index, field, true
	}
	for _, known := range openAITextFields {
		if field == known {
			return index, field, true
		}
	}
	return 0, "", false
}

// findOpenAIChoice 返回 index 匹配的 choice 数组位置及其 delta 成员（缺失时为 nil）。
// delta 存在但不是对象时 ok 为 false。
func findOpenAIChoice(event []byte, index int) (int, map[string]json.RawMessage, bool) {
	var chunk openAIChunk
	if err := json.Unmarshal(event, &chunk); err != nil {
		return 0, nil, false
	}
	for pos, rawChoice := range chunk.Choices {
		var choice openAIChoice
		if json.Unmarshal(rawChoice, &choice) != nil || indexOr(choice.Index, pos) != index {
			continue
		}
		if len(choice.Delta) == 0 {
			return pos, nil, true
		}
		var delta map[string]json.RawMessage
		if json.Unmarshal(choice.Delta, &delta) != nil || delta == nil {
			return 0, nil, false
		}
		return pos, delta, true
	}
	return 0, nil, false
}

// insertInto 把 fragment 作为首个成员/元素插入 path 处的容器。
func insertInto(event []byte, path []string, fragment []byte) ([]byte, bool) {
	open, empty, ok := locateContainer(event, path)
	if !ok {
		return event, false
	}
	out := insertAt(event, open, fragment, !empty)
	if !json.Valid(out) {
		return event, false
	}
	return out, true
}

// indexOr 把 JSON 整数解析为下标，缺失或非整数时返回 fallback。
func indexOr(raw json.RawMessage, fallback int) int {
	if n, err := strconv.Atoi(string(bytes.TrimSpace(raw))); err == nil {
		return n
	}
	return fallback
}

func isJSONString(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}

// finishSet 判断 finish_reason 是否已设置（非缺失、非 null、非空字符串）。
func finishSet(raw json.RawMessage) bool {
	value := string(bytes.TrimSpace(raw))
	return value != "" && value != "null" && value != `""`
}
