package redact

import (
	"encoding/json"
	"strconv"
)

// Responses（openai-response）文本流键格式（3.9 的 restoreEvent / 事件名结束依赖此约定）：
//
//	o<output_index>/c<content_index>/output_text
//	o<output_index>/c<content_index>/refusal
//	o<output_index>/c<content_index>/reasoning_text
//	o<output_index>/c<summary_index>/reasoning_summary_text
//	o<output_index>/args           （function_call_arguments）
//	o<output_index>/custom_input   （custom_tool_call_input）
//
// 键的"kind"即最后一个 "/" 之后的段（output_text/refusal/reasoning_text/
// reasoning_summary_text/args/custom_input），responsesEndByEventName 按 kind 结束。
// 索引缺失或非整数时按 0 处理。

// responsesKind 描述一类 *.delta / *.done 事件对应的文本流 kind 与键形状。
type responsesKind struct {
	kind    string
	content bool // 键是否带 c<index> 段
	summary bool // c 段取 summary_index（否则取 content_index）
}

// responsesDeltaKinds 把 *.delta 事件类型映射到 kind。
var responsesDeltaKinds = map[string]responsesKind{
	"response.output_text.delta":             {kind: "output_text", content: true},
	"response.refusal.delta":                 {kind: "refusal", content: true},
	"response.reasoning_text.delta":          {kind: "reasoning_text", content: true},
	"response.reasoning_summary_text.delta":  {kind: "reasoning_summary_text", content: true, summary: true},
	"response.function_call_arguments.delta": {kind: "args"},
	"response.custom_tool_call_input.delta":  {kind: "custom_input"},
}

// responsesDoneKinds 把 *.done 事件类型映射到 kind（结束同一键）。
var responsesDoneKinds = map[string]responsesKind{
	"response.output_text.done":             {kind: "output_text", content: true},
	"response.refusal.done":                 {kind: "refusal", content: true},
	"response.reasoning_text.done":          {kind: "reasoning_text", content: true},
	"response.reasoning_summary_text.done":  {kind: "reasoning_summary_text", content: true, summary: true},
	"response.reasoning_summary_part.done":  {kind: "reasoning_summary_text", content: true, summary: true},
	"response.function_call_arguments.done": {kind: "args"},
	"response.custom_tool_call_input.done":  {kind: "custom_input"},
}

// responsesEndAllTypes 是结束全部文本流的事件类型。
var responsesEndAllTypes = map[string]bool{
	"response.output_item.done": true,
	"response.completed":        true,
	"response.incomplete":       true,
	"response.failed":           true,
}

type responsesEvent struct {
	Type         string          `json:"type"`
	OutputIndex  json.RawMessage `json:"output_index"`
	ContentIndex json.RawMessage `json:"content_index"`
	SummaryIndex json.RawMessage `json:"summary_index"`
	Delta        json.RawMessage `json:"delta"`
}

// key 按 kind 形状生成文本流键。
func (ev responsesEvent) key(k responsesKind) string {
	out := ev.outputPrefix()
	if k.content {
		index := ev.ContentIndex
		if k.summary {
			index = ev.SummaryIndex
		}
		out += "c" + strconv.Itoa(indexOr(index, 0)) + "/"
	}
	return out + k.kind
}

func (ev responsesEvent) outputPrefix() string {
	return "o" + strconv.Itoa(indexOr(ev.OutputIndex, 0)) + "/"
}

// planResponses 解读 Responses SSE 单个 data 载荷（键格式见文件头注释）。
//   - 六类 *.delta 且 delta 为字符串 → targets（路径 "delta"）。
//   - 对应 *.done（含 reasoning_summary_part.done）→ endKeys 同键。
//   - response.content_part.done → endPrefixes "o<oi>/c<ci>/"。
//   - response.output_item.done → endPrefixes "o<oi>/"。
//   - response.completed / incomplete / failed → endAll。
//
// response.completed 等事件内的完整文本不是 target，由调用方做完整占位符还原。
// 其他类型 ok 为 true 但无目标；非对象或非法 JSON 时 ok 为 false。
func planResponses(event []byte) (eventPlan, bool) {
	var ev responsesEvent
	if err := json.Unmarshal(event, &ev); err != nil {
		return eventPlan{}, false
	}
	var plan eventPlan
	if k, ok := responsesDeltaKinds[ev.Type]; ok {
		if isJSONString(ev.Delta) {
			plan.targets = []textTarget{{path: joinPath("delta"), key: ev.key(k)}}
		}
		return plan, true
	}
	if k, ok := responsesDoneKinds[ev.Type]; ok {
		plan.endKeys = []string{ev.key(k)}
		return plan, true
	}
	switch ev.Type {
	case "response.content_part.done":
		plan.endPrefixes = []string{ev.outputPrefix() + "c" + strconv.Itoa(indexOr(ev.ContentIndex, 0)) + "/"}
	case "response.output_item.done":
		plan.endPrefixes = []string{ev.outputPrefix()}
	case "response.completed", "response.incomplete", "response.failed":
		plan.endAll = true
	}
	return plan, true
}

// responsesContentPartKinds 是 content_part 下可能出现的 kind。
var responsesContentPartKinds = []string{"output_text", "refusal", "reasoning_text"}

// responsesEndByEventName 供 Codex 单行模式使用：`event: <name>` 行先于其 data 行单独到达，
// 此时不知道 output_index，只能按 kind 结束。调用方应结束所有 kind（键最后一个 "/" 之后的段）
// 属于返回列表的残留键；all 为真时结束全部键。未知事件名返回 (nil, false)。
//   - *.done（六类 + reasoning_summary_part.done）→ 对应单个 kind
//   - response.content_part.done → output_text / refusal / reasoning_text
//   - response.output_item.done / completed / incomplete / failed → all
func responsesEndByEventName(name string) ([]string, bool) {
	if responsesEndAllTypes[name] {
		return nil, true
	}
	if k, ok := responsesDoneKinds[name]; ok {
		return []string{k.kind}, false
	}
	if name == "response.content_part.done" {
		return append([]string(nil), responsesContentPartKinds...), false
	}
	return nil, false
}

// synthResponses 用模板（该键最近一次 *.delta 事件）合成只携带 carry 的增量帧：
// 仅替换 tpl.path 处的字符串，返回 "event: <name>\ndata: <json>\n\n"，
// name 取 tpl.event，为空时取模板 JSON 的 type。模板 sequence_number 原样复用（已知限制）。
// 模板为空、非法、路径处没有字符串或无法确定事件名时返回 nil。
func synthResponses(tpl streamTemplate, carry string) []byte {
	if len(tpl.raw) == 0 || len(tpl.path) == 0 {
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
	name := tpl.event
	if name == "" {
		var ev responsesEvent
		if json.Unmarshal(tpl.raw, &ev) != nil || ev.Type == "" {
			return nil
		}
		name = ev.Type
	}
	out := make([]byte, 0, len(body)+len(name)+16)
	out = append(out, "event: "+name+"\ndata: "...)
	out = append(out, body...)
	return append(out, "\n\n"...)
}
