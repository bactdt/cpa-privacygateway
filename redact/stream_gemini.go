package redact

import (
	"encoding/json"
	"strconv"
	"strings"
)

type geminiChunk struct {
	Candidates []json.RawMessage `json:"candidates"`
}

type geminiCandidate struct {
	Index        json.RawMessage `json:"index"`
	Content      json.RawMessage `json:"content"`
	FinishReason json.RawMessage `json:"finishReason"`
}

type geminiContent struct {
	Parts []json.RawMessage `json:"parts"`
}

type geminiPart struct {
	Text    json.RawMessage `json:"text"`
	Thought json.RawMessage `json:"thought"`
}

// planGemini 解读 Gemini streamGenerateContent 的裸 JSON 事件。
// candidates[i].content.parts[j].text 为字符串时作为目标，键为 "g<index>/text"，
// part.thought 为 true 时为 "g<index>/thought"；index 缺失或非整数时用数组位置。
// functionCall 等非 text part 不作为目标（走完整占位符还原）。
// finishReason 非空 → endPrefixes 追加 "g<index>/"。
// 非对象或没有 candidates 数组（含仅 usageMetadata 的块）时 ok 为 false。
func planGemini(event []byte) (eventPlan, bool) {
	var chunk geminiChunk
	if err := json.Unmarshal(event, &chunk); err != nil || len(chunk.Candidates) == 0 {
		return eventPlan{}, false
	}
	var plan eventPlan
	for pos, rawCandidate := range chunk.Candidates {
		var candidate geminiCandidate
		if err := json.Unmarshal(rawCandidate, &candidate); err != nil {
			continue
		}
		prefix := "g" + strconv.Itoa(indexOr(candidate.Index, pos)) + "/"
		plan.targets = append(plan.targets, geminiPartTargets(candidate.Content, strconv.Itoa(pos), prefix)...)
		if finishSet(candidate.FinishReason) {
			plan.endPrefixes = append(plan.endPrefixes, prefix)
		}
	}
	return plan, true
}

func geminiPartTargets(rawContent json.RawMessage, pos, prefix string) []textTarget {
	var content geminiContent
	if err := json.Unmarshal(rawContent, &content); err != nil {
		return nil
	}
	var targets []textTarget
	for j, rawPart := range content.Parts {
		var part geminiPart
		if json.Unmarshal(rawPart, &part) != nil || !isJSONString(part.Text) {
			continue
		}
		field := "text"
		if isThought(part.Thought) {
			field = "thought"
		}
		path := joinPath("candidates", pos, "content", "parts", strconv.Itoa(j), "text")
		targets = append(targets, textTarget{path: path, key: prefix + field})
	}
	return targets
}

func isThought(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "true"
}

// injectGemini 把残留 carry 作为新 part 注入结束事件中 key 对应的 candidate：
// 已有 content.parts 数组时在开头插入 {"text":carry}（thought 键追加 "thought":true）；
// content 缺 parts 时插入 "parts":[...]；candidate 缺 content 时插入
// "content":{"role":"model","parts":[...]}。无法定位时返回 (event, false)。
func injectGemini(event []byte, key, carry string) ([]byte, bool) {
	index, field, ok := parseGeminiKey(key)
	if !ok {
		return event, false
	}
	pos, content, ok := findGeminiCandidate(event, index)
	if !ok {
		return event, false
	}
	part := `{"text":` + string(encodeTextLiteral(carry))
	if field == "thought" {
		part += `,"thought":true`
	}
	part += `}`

	candidatePath := []string{"candidates", strconv.Itoa(pos)}
	contentPath := append(append([]string{}, candidatePath...), "content")
	if content == nil {
		return insertInto(event, candidatePath, []byte(`"content":{"role":"model","parts":[`+part+`]}`))
	}
	if raw, present := content["parts"]; present {
		if len(raw) == 0 || raw[0] != '[' {
			return event, false
		}
		return insertInto(event, append(contentPath, "parts"), []byte(part))
	}
	return insertInto(event, contentPath, []byte(`"parts":[`+part+`]`))
}

// parseGeminiKey 解析 "g<index>/text" 或 "g<index>/thought"。
func parseGeminiKey(key string) (int, string, bool) {
	rest, ok := strings.CutPrefix(key, "g")
	if !ok {
		return 0, "", false
	}
	num, field, ok := strings.Cut(rest, "/")
	index, err := strconv.Atoi(num)
	if !ok || err != nil || (field != "text" && field != "thought") {
		return 0, "", false
	}
	return index, field, true
}

// findGeminiCandidate 返回 index 匹配的 candidate 数组位置及其 content 成员（缺失时为 nil）。
// content 存在但不是对象时 ok 为 false。
func findGeminiCandidate(event []byte, index int) (int, map[string]json.RawMessage, bool) {
	var chunk geminiChunk
	if err := json.Unmarshal(event, &chunk); err != nil {
		return 0, nil, false
	}
	for pos, rawCandidate := range chunk.Candidates {
		var candidate geminiCandidate
		if json.Unmarshal(rawCandidate, &candidate) != nil || indexOr(candidate.Index, pos) != index {
			continue
		}
		if len(candidate.Content) == 0 {
			return pos, nil, true
		}
		var content map[string]json.RawMessage
		if json.Unmarshal(candidate.Content, &content) != nil || content == nil {
			return 0, nil, false
		}
		return pos, content, true
	}
	return 0, nil, false
}
