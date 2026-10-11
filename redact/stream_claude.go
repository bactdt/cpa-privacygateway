package redact

import (
	"encoding/json"
	"strconv"
)

// claudeTextFields 是 content_block_delta.delta 下按文本流处理的字符串字段
// （text_delta / thinking_delta / input_json_delta）。
var claudeTextFields = []string{"text", "thinking", "partial_json"}

type claudeEvent struct {
	Type  string          `json:"type"`
	Index json.RawMessage `json:"index"`
	Delta json.RawMessage `json:"delta"`
}

// planClaude 解读 Claude SSE 单个 data 载荷。
// content_block_delta 的 delta.text/thinking/partial_json（以字符串出现者）→ 键 "b<index>"；
// content_block_stop → endKeys ["b<index>"]；message_delta / message_stop → endAll。
// 其他事件类型 ok 为 true 但无目标；非对象或非法 JSON 时 ok 为 false。
func planClaude(event []byte) (eventPlan, bool) {
	var ev claudeEvent
	if err := json.Unmarshal(event, &ev); err != nil {
		return eventPlan{}, false
	}
	var plan eventPlan
	key := "b" + strconv.Itoa(indexOr(ev.Index, 0))
	switch ev.Type {
	case "content_block_delta":
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(ev.Delta, &delta); err != nil || delta == nil {
			return plan, true
		}
		for _, field := range claudeTextFields {
			if isJSONString(delta[field]) {
				plan.targets = append(plan.targets, textTarget{path: joinPath("delta", field), key: key})
				break
			}
		}
	case "content_block_stop":
		plan.endKeys = []string{key}
	case "message_delta", "message_stop":
		plan.endAll = true
	}
	return plan, true
}

// synthClaude 用模板（该键最近一次 content_block_delta）合成只携带 carry 的增量帧：
// 仅替换 tpl.path 处的字符串，返回 "event: <name>\ndata: <json>\n\n"。
// 模板为空、非法或路径处没有字符串时返回 nil。
func synthClaude(tpl streamTemplate, carry string) []byte {
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
		name = "content_block_delta"
	}
	out := make([]byte, 0, len(body)+len(name)+16)
	out = append(out, "event: "+name+"\ndata: "...)
	out = append(out, body...)
	return append(out, "\n\n"...)
}
