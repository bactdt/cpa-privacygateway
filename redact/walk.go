package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

var ErrMalformedJSON = errors.New("request body is not valid JSON")

type WalkOptions struct {
	Format              string
	RedactToolArguments bool
	MaxDepth            int
	// OnFinding 非 nil 时，RedactJSON 每写入一个占位符回调一次（需求 4.1）；
	// 为 nil 时不调用 MaskPreview/RenderPath，输出与未接线时逐字节相同（需求 4.4）。
	OnFinding FindingSink
	// OnStale 非 nil 时，对被扫描字段中 nonce 不属于本请求的占位符逐个回调；
	// 参数为字段路径与掩码样例（[[PG_<4 位>…_<seq>]]），不含原值。
	OnStale func(path []string, sample string)
}

type replacement struct {
	start int
	end   int
	value []byte
}

var textKeys = map[string]bool{
	"text":         true,
	"content":      true,
	"prompt":       true,
	"instructions": true,
	"system":       true,
	"description":  true,
	"query":        true,
	"input_text":   true,
	"output_text":  true,
	"title":        true,
	"input":        true,
}

var structuralKeys = map[string]bool{
	"type":              true,
	"role":              true,
	"model":             true,
	"id":                true,
	"name":              true,
	"url":               true,
	"format":            true,
	"mime_type":         true,
	"media_type":        true,
	"data":              true,
	"signature":         true,
	"thinking":          true,
	"reasoning":         true,
	"reasoning_content": true,
	"encrypted_content": true,
	"tool_call_id":      true,
	"tool_use_id":       true,
	"image_url":         true,
	"b64_json":          true,
	"finish_reason":     true,
	"stop_reason":       true,
	"object":            true,
	"cache_control":     true,
}

func toolArgKeys(format string) map[string]bool {
	switch format {
	case "claude":
		return map[string]bool{"arguments": true, "args": true, "input": true}
	case "gemini":
		return map[string]bool{"args": true}
	default:
		return map[string]bool{"arguments": true}
	}
}

func RedactJSON(format string, body []byte, m *Mapping, cfg Config, opts WalkOptions) ([]byte, int, error) {
	toolKeys := map[string]bool{}
	if opts.RedactToolArguments {
		toolKeys = toolArgKeys(format)
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 64
	}
	visitor := func(path []string, toolArg, arrayText bool, value string) (string, bool, error) {
		if toolArg {
			reportStale(path, value, m, opts.OnStale)
			replaced, count, err := m.RedactObserved(value, 0, cfg, path, opts.OnFinding)
			if err != nil {
				return "", false, err
			}
			if count == 0 {
				return "", false, nil
			}
			return replaced, true, nil
		}
		key := lastKey(path)
		if structuralKeys[key] {
			return "", false, nil
		}
		if !textKeys[key] && !arrayText {
			return "", false, nil
		}
		reportStale(path, value, m, opts.OnStale)
		replaced, count, err := m.RedactObserved(value, 0, cfg, path, opts.OnFinding)
		if err != nil {
			return "", false, err
		}
		if count == 0 {
			return "", false, nil
		}
		return replaced, true, nil
	}
	scanToolOutput := format == formatResponses || format == "codex"
	rebuilt, count, err := transformJSON(body, maxDepth, toolKeys, scanToolOutput, visitor)
	if err != nil {
		return nil, 0, err
	}
	return rebuilt, count, nil
}

func RestoreJSON(body []byte, m *Mapping) ([]byte, int, error) {
	if m.Size() == 0 {
		return body, 0, nil
	}
	visitor := func(_ []string, _, _ bool, value string) (string, bool, error) {
		replaced := m.Restore(value)
		if replaced == value {
			return "", false, nil
		}
		return replaced, true, nil
	}
	return transformJSON(body, 64, nil, false, visitor)
}

// RestoreJSONCounted 是 RestoreJSON 的观测版本：输出逐字节相同（需求 4.7），
// 并汇总已还原/未知占位符统计（需求 4.6，未知样本至多 5 个）。
func RestoreJSONCounted(body []byte, m *Mapping) ([]byte, RestoreJSONStats, error) {
	var stats RestoreJSONStats
	if m.Size() == 0 {
		return body, stats, nil
	}
	visitor := func(_ []string, _, _ bool, value string) (string, bool, error) {
		replaced, s := m.RestoreCounted(value)
		stats.Restored += s.Restored
		stats.Unknown += s.Unknown
		for _, sample := range s.UnknownSamples {
			if len(stats.UnknownSamples) < 5 {
				stats.UnknownSamples = append(stats.UnknownSamples, sample)
			}
		}
		if replaced == value {
			return "", false, nil
		}
		return replaced, true, nil
	}
	out, _, err := transformJSON(body, 64, nil, false, visitor)
	return out, stats, err
}

func lastKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return path[len(path)-1]
}

type offsetDecoder struct {
	dec   *json.Decoder
	start int64
	end   int64
}

func (d *offsetDecoder) token() (json.Token, error) {
	d.start = d.dec.InputOffset()
	tok, err := d.dec.Token()
	d.end = d.dec.InputOffset()
	return tok, err
}

func transformJSON(body []byte, maxDepth int, toolArgKeys map[string]bool, scanToolOutput bool, visit func(path []string, toolArg, arrayText bool, value string) (string, bool, error)) ([]byte, int, error) {
	dec := &offsetDecoder{dec: json.NewDecoder(bytes.NewReader(body))}
	var replacements []replacement
	if err := walkValue(dec, body, nil, false, false, toolArgKeys, maxDepth, scanToolOutput, visit, &replacements); err != nil {
		if errors.Is(err, ErrCapacity) || errors.Is(err, ErrFindings) {
			return nil, 0, err
		}
		if errors.Is(err, io.EOF) {
			return nil, 0, ErrMalformedJSON
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if _, err := dec.token(); !errors.Is(err, io.EOF) {
		return nil, 0, ErrMalformedJSON
	}
	if len(replacements) == 0 {
		return body, 0, nil
	}
	return applyReplacements(body, replacements), len(replacements), nil
}

func walkValue(dec *offsetDecoder, body []byte, path []string, toolArg, arrayText bool, toolArgKeys map[string]bool, depth int, scanToolOutput bool, visit func([]string, bool, bool, string) (string, bool, error), out *[]replacement) error {
	if depth <= 0 {
		return errors.New("json nesting too deep")
	}
	tok, err := dec.token()
	if err != nil {
		return err
	}
	switch value := tok.(type) {
	case json.Delim:
		switch value {
		case '{':
			scanOutput := scanToolOutput && isInputEntry(path)
			objectType := ""
			var deferred *deferredString
			for dec.dec.More() {
				keyToken, err := dec.token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid object key")
				}
				childToolArg := toolArg
				if !toolArg && toolArgKeys[key] {
					childToolArg = true
				}
				childPath := appendPath(path, key)
				if scanOutput && (key == "type" || key == "output") {
					tok, err := dec.token()
					if err != nil {
						return err
					}
					text, isString := tok.(string)
					if key == "type" {
						if isString {
							objectType = text
						} else if err := skipValue(dec, tok, depth-1); err != nil {
							return err
						}
						continue
					}
					if isString {
						deferred = &deferredString{
							path: childPath, toolArg: childToolArg, value: text,
							start: dec.start, end: dec.end,
						}
					} else if err := skipValue(dec, tok, depth-1); err != nil {
						return err
					}
					continue
				}
				// 对象子树不继承数组文本上下文，避免 text 键数组内的
				// 结构对象字段被误脱敏（只扫直接字符串元素）。
				if err := walkValue(dec, body, childPath, childToolArg, false, toolArgKeys, depth-1, scanToolOutput, visit, out); err != nil {
					return err
				}
			}
			if _, err := dec.token(); err != nil {
				return err
			}
			if deferred != nil {
				forceText := objectType == "function_call_output" || objectType == "custom_tool_call_output"
				replaced, ok, err := visit(deferred.path, deferred.toolArg, forceText, deferred.value)
				if err != nil {
					return err
				}
				if ok {
					if err := appendStringReplacement(out, body, deferred.start, deferred.end, replaced); err != nil {
						return err
					}
				}
			}
			return nil
		case '[':
			index := 0
			// 数组的直接字符串元素按数组文本上下文扫描；
			// 上下文不穿过对象继续传播。
			directArrayText := arrayText || textKeys[lastKey(path)]
			for dec.dec.More() {
				childPath := appendPath(path, strconv.Itoa(index))
				if err := walkValue(dec, body, childPath, toolArg, directArrayText, toolArgKeys, depth-1, scanToolOutput, visit, out); err != nil {
					return err
				}
				index++
			}
			_, err := dec.token()
			return err
		}
		return nil
	case string:
		replaced, ok, err := visit(path, toolArg, arrayText, value)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return appendStringReplacement(out, body, dec.start, dec.end, replaced)
	default:
		return nil
	}
}

// isInputEntry 判断路径是否为 Responses 请求 input 数组的直接元素（input[i]）。
func isInputEntry(path []string) bool {
	return len(path) == 2 && path[0] == "input" && isArrayIndex(path[1])
}

type deferredString struct {
	path    []string
	toolArg bool
	value   string
	start   int64
	end     int64
}

// skipValue 消费已读出的首个 token 及其剩余子树。
func skipValue(dec *offsetDecoder, tok json.Token, depth int) error {
	if depth <= 0 {
		return errors.New("json nesting too deep")
	}
	delim, ok := tok.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil
	}
	for dec.dec.More() {
		child, err := dec.token()
		if err != nil {
			return err
		}
		if err := skipValue(dec, child, depth-1); err != nil {
			return err
		}
	}
	_, err := dec.token()
	return err
}

// appendStringReplacement 把字符串 token 的替换结果按字面量区间追加到 out。
func appendStringReplacement(out *[]replacement, body []byte, start, end int64, replaced string) error {
	literalStart := int(start)
	window := body[literalStart:int(end)]
	quote := bytes.IndexByte(window, '"')
	if quote < 0 {
		return nil
	}
	literalStart += quote
	encoded, err := encodeStringLiteral(replaced)
	if err != nil {
		return err
	}
	*out = append(*out, replacement{start: literalStart, end: int(end), value: encoded})
	return nil
}

// reportStale 对被扫描字段中其他 nonce 的占位符逐个回调，样例只含掩码。
func reportStale(path []string, value string, m *Mapping, sink func([]string, string)) {
	if sink == nil || m == nil {
		return
	}
	nonce := m.Nonce()
	for _, token := range placeholderRe.FindAllString(value, -1) {
		tokenNonce := token[len(placeholderOpen) : len(placeholderOpen)+placeholderNonceHexLen]
		if tokenNonce == nonce {
			continue
		}
		seq := token[len(placeholderOpen)+placeholderNonceHexLen+1 : len(token)-len(placeholderClose)]
		sink(path, placeholderOpen+tokenNonce[:4]+"…_"+seq+placeholderClose)
	}
}

func appendPath(path []string, element string) []string {
	next := make([]string, len(path)+1)
	copy(next, path)
	next[len(path)] = element
	return next
}

func encodeStringLiteral(value string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func applyReplacements(body []byte, replacements []replacement) []byte {
	sort.SliceStable(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})
	var builder bytes.Buffer
	builder.Grow(len(body))
	cursor := 0
	for _, item := range replacements {
		if item.start < cursor {
			continue
		}
		builder.Write(body[cursor:item.start])
		builder.Write(item.value)
		cursor = item.end
	}
	builder.Write(body[cursor:])
	return builder.Bytes()
}
