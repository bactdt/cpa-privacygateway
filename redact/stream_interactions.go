package redact

import (
	"encoding/json"
	"strconv"
)

// interactions 事件结构（CPA /v1beta/interactions）：
//   - step.delta：delta.type 为 text 时文本在 delta.text；thought_summary 时在
//     delta.content.text；arguments_delta 时工具参数在 delta.arguments（字符串）。
//   - step.stop：结束该 step（index）的全部文本流。
//   - interaction.completed / done（[DONE] 载荷）：结束全部文本流。
// 文本流键为 s<index>/text、s<index>/thought、s<index>/args。

type interactionsEvent struct {
	EventType string          `json:"event_type"`
	Index     json.RawMessage `json:"index"`
	Delta     json.RawMessage `json:"delta"`
}

type interactionsDelta struct {
	Type      string          `json:"type"`
	Text      json.RawMessage `json:"text"`
	Content   json.RawMessage `json:"content"`
	Arguments json.RawMessage `json:"arguments"`
}

// planInteractions 解读 interactions SSE 单个 data 载荷。
func planInteractions(event []byte) (eventPlan, bool) {
	var ev interactionsEvent
	if err := json.Unmarshal(event, &ev); err != nil {
		return eventPlan{}, false
	}
	prefix := "s" + strconv.Itoa(indexOr(ev.Index, 0)) + "/"
	var plan eventPlan
	switch ev.EventType {
	case "step.delta":
		var delta interactionsDelta
		if err := json.Unmarshal(ev.Delta, &delta); err != nil {
			return plan, true
		}
		switch delta.Type {
		case "text":
			if isJSONString(delta.Text) {
				plan.targets = append(plan.targets, textTarget{path: joinPath("delta", "text"), key: prefix + "text"})
			}
		case "thought_summary":
			var content map[string]json.RawMessage
			if json.Unmarshal(delta.Content, &content) == nil && isJSONString(content["text"]) {
				plan.targets = append(plan.targets, textTarget{
					path: joinPath("delta", "content", "text"), key: prefix + "thought",
				})
			}
		case "arguments_delta":
			if isJSONString(delta.Arguments) {
				plan.targets = append(plan.targets, textTarget{path: joinPath("delta", "arguments"), key: prefix + "args"})
			}
		}
	case "step.stop":
		plan.endPrefixes = []string{prefix}
	case "interaction.completed", "done":
		plan.endAll = true
	}
	return plan, true
}

// synthInteractions 用模板合成只携带 carry 的 step.delta 帧。
func synthInteractions(tpl streamTemplate, carry string) []byte {
	name := tpl.event
	if name == "" {
		name = "step.delta"
	}
	return synthSSEPayload(tpl, carry, name)
}

// deliverAllFrames 取出全部残留并逐个合成帧（用于 interactions 的 done/[DONE]）。
func (s *StreamRestorer) deliverAllFrames() []byte {
	taken := s.text.takeCarry(nil, "", true)
	var frames []byte
	for _, key := range sortedKeys(taken) {
		frame, ok := s.synthFrame(formatInteractions, key, taken[key])
		frames = append(frames, frame...)
		s.countDelivery(ok, taken[key])
	}
	return frames
}
