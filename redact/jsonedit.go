package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// jsonEditMaxDepth 是定点编辑原语允许的最大嵌套深度，与 RestoreJSON 一致。
const jsonEditMaxDepth = 64

var errJSONTooDeep = errors.New("json nesting too deep")

// editWalker 在一次遍历中同时服务字符串改写与容器定位。
type editWalker struct {
	dec         *offsetDecoder
	body        []byte
	onString    func(path []string, value string, start, end int)
	onContainer func(path []string, open int)
}

// editStrings 遍历所有 JSON 字符串值（不含对象键），visit 返回 (新值, true)
// 时把该字面量原位替换为 encodeTextLiteral(新值)；其余字节保持不变。
// 无替换时返回原切片与 false；非法 JSON 或嵌套超过 64 层返回错误。
func editStrings(body []byte, visit func(path []string, value string) (string, bool)) ([]byte, bool, error) {
	var replacements []replacement
	w := &editWalker{
		body: body,
		onString: func(path []string, value string, start, end int) {
			next, ok := visit(path, value)
			if !ok {
				return
			}
			replacements = append(replacements, replacement{start: start, end: end, value: encodeTextLiteral(next)})
		},
	}
	if err := w.run(); err != nil {
		return nil, false, err
	}
	if len(replacements) == 0 {
		return body, false, nil
	}
	return applyReplacements(body, replacements), true, nil
}

// locateContainer 返回 path 处对象/数组 `{`/`[` 之后一字节的偏移及容器是否为空。
// path 不存在、目标不是容器或 JSON 非法时 ok 为 false。
func locateContainer(body []byte, path []string) (open int, empty bool, ok bool) {
	open = -1
	w := &editWalker{
		body: body,
		onContainer: func(p []string, at int) {
			if open < 0 && pathEqual(p, path) {
				open = at
			}
		},
	}
	if err := w.run(); err != nil || open < 0 {
		return 0, false, false
	}
	rest := bytes.TrimLeft(body[open:], " \t\r\n")
	empty = len(rest) > 0 && (rest[0] == '}' || rest[0] == ']')
	return open, empty, true
}

// insertAt 在 offset 处插入 fragment，返回新切片；needComma 时在 fragment
// 之后追加 ","，用于插到非空容器的首个成员之前。
func insertAt(body []byte, offset int, fragment []byte, needComma bool) []byte {
	size := len(body) + len(fragment)
	if needComma {
		size++
	}
	out := make([]byte, 0, size)
	out = append(out, body[:offset]...)
	out = append(out, fragment...)
	if needComma {
		out = append(out, ',')
	}
	return append(out, body[offset:]...)
}

// encodeTextLiteral 把文本编码为 JSON 字符串字面量，不转义 `<>&`。
func encodeTextLiteral(value string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// 编码 string 不会失败（非法 UTF-8 会被替换为 U+FFFD）。
	_ = enc.Encode(value)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func (w *editWalker) run() error {
	w.dec = &offsetDecoder{dec: json.NewDecoder(bytes.NewReader(w.body))}
	if err := w.walk(nil, jsonEditMaxDepth); err != nil {
		if errors.Is(err, errJSONTooDeep) {
			return err
		}
		if errors.Is(err, io.EOF) {
			return ErrMalformedJSON
		}
		return fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if _, err := w.dec.token(); !errors.Is(err, io.EOF) {
		return ErrMalformedJSON
	}
	return nil
}

func (w *editWalker) walk(path []string, depth int) error {
	if depth <= 0 {
		return errJSONTooDeep
	}
	tok, err := w.dec.token()
	if err != nil {
		return err
	}
	switch value := tok.(type) {
	case json.Delim:
		if w.onContainer != nil {
			w.onContainer(path, int(w.dec.end))
		}
		if value == '{' {
			return w.walkObject(path, depth)
		}
		return w.walkArray(path, depth)
	case string:
		if w.onString == nil {
			return nil
		}
		start := int(w.dec.start)
		quote := bytes.IndexByte(w.body[start:int(w.dec.end)], '"')
		if quote < 0 {
			return nil
		}
		w.onString(path, value, start+quote, int(w.dec.end))
	}
	return nil
}

func (w *editWalker) walkObject(path []string, depth int) error {
	for w.dec.dec.More() {
		keyToken, err := w.dec.token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid object key")
		}
		if err := w.walk(appendPath(path, key), depth-1); err != nil {
			return err
		}
	}
	_, err := w.dec.token()
	return err
}

func (w *editWalker) walkArray(path []string, depth int) error {
	for index := 0; w.dec.dec.More(); index++ {
		if err := w.walk(appendPath(path, strconv.Itoa(index)), depth-1); err != nil {
			return err
		}
	}
	_, err := w.dec.token()
	return err
}

func pathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
