package redact

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	placeholderOpen        = "[[PG_"
	placeholderClose       = "]]"
	placeholderNonceHexLen = 16
	placeholderMaxSeqLen   = 9
)

var (
	ErrCapacity   = errors.New("placeholder mapping capacity exceeded")
	ErrFindings   = errors.New("detector findings exceeded max_findings")
	placeholderRe = regexp.MustCompile(regexp.QuoteMeta(placeholderOpen) + `[0-9a-f]{16}_[0-9]{1,9}` + regexp.QuoteMeta(placeholderClose))
)

func formatPlaceholder(nonce string, seq int) string {
	return placeholderOpen + nonce + "_" + strconv.Itoa(seq) + placeholderClose
}

func placeholderPrefixFor(nonce string) []byte {
	return []byte(placeholderOpen + nonce + "_")
}

type Mapping struct {
	mu         sync.Mutex
	nonce      string
	capacity   int
	next       int
	rawToPh    map[string]string
	phToRaw    map[string]string
	phToEsc    map[string]string
	maxRawLen  int
	totalBytes int
	maxBytes   int
}

func NewMapping(capacity, maxBytes int) (*Mapping, error) {
	if capacity <= 0 {
		capacity = 1024
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &Mapping{
		nonce:     nonce,
		capacity:  capacity,
		rawToPh:   make(map[string]string),
		phToRaw:   make(map[string]string),
		phToEsc:   make(map[string]string),
		maxBytes:  maxBytes,
		maxRawLen: 1 << 20,
	}, nil
}

func newNonce() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func (m *Mapping) Nonce() string {
	return m.nonce
}

func (m *Mapping) Size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.phToRaw)
}

func (m *Mapping) Placeholder(raw string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.rawToPh[raw]; ok {
		return existing, nil
	}
	if len(m.phToRaw) >= m.capacity {
		return "", ErrCapacity
	}
	if len(raw) > m.maxRawLen {
		return "", ErrCapacity
	}
	if m.totalBytes+len(raw) > m.maxBytes {
		return "", ErrCapacity
	}
	m.next++
	placeholder := formatPlaceholder(m.nonce, m.next)
	escaped, err := jsonEscape(raw)
	if err != nil {
		return "", err
	}
	m.rawToPh[raw] = placeholder
	m.phToRaw[placeholder] = raw
	m.phToEsc[placeholder] = escaped
	m.totalBytes += len(raw)
	return placeholder, nil
}

func (m *Mapping) Redact(text string, base int, cfg Config) (string, int, error) {
	raw, truncated := cfg.Detect(text, base, nil)
	spans := resolveSpans(raw)
	if truncated {
		return "", 0, ErrFindings
	}
	if len(spans) == 0 {
		return text, 0, nil
	}
	existing := placeholderRe.FindAllStringIndex(text, -1)
	var builder strings.Builder
	builder.Grow(len(text))
	cursor := 0
	count := 0
	for _, item := range spans {
		start := item.start - base
		end := item.end - base
		if start < cursor || end > len(text) {
			continue
		}
		if overlapsRanges(existing, start, end) {
			continue
		}
		raw := text[start:end]
		placeholder, err := m.Placeholder(raw)
		if err != nil {
			return "", 0, err
		}
		builder.WriteString(text[cursor:start])
		builder.WriteString(placeholder)
		cursor = end
		count++
	}
	builder.WriteString(text[cursor:])
	return builder.String(), count, nil
}

func (m *Mapping) Restore(text string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.phToRaw) == 0 {
		return text
	}
	return placeholderRe.ReplaceAllStringFunc(text, func(token string) string {
		if raw, ok := m.phToRaw[token]; ok {
			return raw
		}
		return token
	})
}

// RestoreCount 与 Restore 相同地还原占位符，并返回已还原个数与
// "本 nonce 但序号不在映射中"的未知占位符个数；其他 nonce 的占位符不计数。
func (m *Mapping) RestoreCount(text string) (string, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.phToRaw) == 0 && !strings.Contains(text, placeholderOpen) {
		return text, 0, 0
	}
	prefix := string(placeholderPrefixFor(m.nonce))
	restored, unknown := 0, 0
	out := placeholderRe.ReplaceAllStringFunc(text, func(token string) string {
		if raw, ok := m.phToRaw[token]; ok {
			restored++
			return raw
		}
		if strings.HasPrefix(token, prefix) {
			unknown++
		}
		return token
	})
	return out, restored, unknown
}

func (m *Mapping) RestoreEscaped(text string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.phToEsc) == 0 {
		return text
	}
	return placeholderRe.ReplaceAllStringFunc(text, func(token string) string {
		if escaped, ok := m.phToEsc[token]; ok {
			return escaped
		}
		return token
	})
}

// RestoreEscapedBytes 还原 SSE data 载荷中的占位符，不破坏 JSON 转义结构：
//   - 整载荷是单个 JSON 字符串字面量时，反转义其内容、还原占位符后重新编码；
//   - 其他载荷只定点还原 `\u005b`/`\u005d`（占位符括号的转义形式），
//     再按 JSON-escaped 形式替换占位符；其余转义序列保持原样。
func (m *Mapping) RestoreEscapedBytes(text []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.phToEsc) == 0 || len(text) == 0 {
		return text
	}
	if inner, ok := unescapeJSONStringPayload(text); ok {
		if !bytesContainsPlaceholderStart(inner) {
			return text
		}
		replaced := m.replacePlaceholders(inner, false)
		encoded, err := jsonEncodeStringBytes(replaced)
		if err != nil {
			return text
		}
		return encoded
	}
	return m.replacePlaceholders(unescapeBracketSequences(text), true)
}

func (m *Mapping) replacePlaceholders(text []byte, escaped bool) []byte {
	if !bytesContainsPlaceholderStart(text) {
		return text
	}
	return placeholderRe.ReplaceAllFunc(text, func(token []byte) []byte {
		if escaped {
			if out, ok := m.phToEsc[string(token)]; ok {
				return []byte(out)
			}
			return token
		}
		if raw, ok := m.phToRaw[string(token)]; ok {
			return []byte(raw)
		}
		return token
	})
}

// unescapeJSONStringPayload 判断整段载荷是否是单个 JSON 字符串字面量
// （首尾各一个未转义引号），是则反转义其内容，否则拒绝。
func unescapeJSONStringPayload(text []byte) ([]byte, bool) {
	if len(text) < 2 || text[0] != '"' {
		return nil, false
	}
	end := len(text) - 1
	if text[end] != '"' {
		return nil, false
	}
	inner := text[1:end]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' {
			i++
			continue
		}
		if inner[i] == '"' {
			return nil, false
		}
	}
	return unescapeJSONBytes(inner), true
}

var bracketEscapePairs = []struct{ seq, lit []byte }{
	{[]byte(`\u005b`), []byte("[")},
	{[]byte(`\u005B`), []byte("[")},
	{[]byte(`\u005d`), []byte("]")},
	{[]byte(`\u005D`), []byte("]")},
}

// unescapeBracketSequences 只把占位符括号的 \u 转义还原为字面字符，
// 不触碰其他任何字节，保证 JSON 结构与其余转义不变。
func unescapeBracketSequences(text []byte) []byte {
	if !hasBracketEscape(text) {
		return text
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		matched := false
		for _, pair := range bracketEscapePairs {
			if i+len(pair.seq) <= len(text) && bytes.Equal(text[i:i+len(pair.seq)], pair.seq) {
				out = append(out, pair.lit...)
				i += len(pair.seq) - 1
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, text[i])
		}
	}
	return out
}

func hasBracketEscape(text []byte) bool {
	for _, pair := range bracketEscapePairs {
		if bytes.Contains(text, pair.seq) {
			return true
		}
	}
	return false
}

func jsonEncodeStringBytes(value []byte) ([]byte, error) {
	raw, err := json.Marshal(string(value))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func unescapeJSONBytes(text []byte) []byte {
	if bytes.IndexByte(text, '\\') < 0 {
		return text
	}
	buf := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			switch text[i+1] {
			case '"', '\\', '/':
				buf = append(buf, text[i+1])
				i++
				continue
			case 'b':
				buf = append(buf, '\b')
				i++
				continue
			case 'f':
				buf = append(buf, '\f')
				i++
				continue
			case 'n':
				buf = append(buf, '\n')
				i++
				continue
			case 'r':
				buf = append(buf, '\r')
				i++
				continue
			case 't':
				buf = append(buf, '\t')
				i++
				continue
			case 'u':
				if i+5 < len(text) {
					var r rune
					for j := 0; j < 4; j++ {
						c := text[i+2+j]
						r <<= 4
						switch {
						case c >= '0' && c <= '9':
							r |= rune(c - '0')
						case c >= 'a' && c <= 'f':
							r |= rune(c - 'a' + 10)
						case c >= 'A' && c <= 'F':
							r |= rune(c - 'A' + 10)
						default:
							r = -1
							break
						}
					}
					if r >= 0 {
						var rbuf [4]byte
						n := utf8.EncodeRune(rbuf[:], r)
						buf = append(buf, rbuf[:n]...)
						i += 5
						continue
					}
				}
			}
		}
		buf = append(buf, text[i])
	}
	return buf
}

func bytesContainsPlaceholderStart(text []byte) bool {
	return bytes.Contains(text, []byte(placeholderOpen))
}

// Has 报告占位符是否可还原，供宿主在响应侧预检（TD-CPA-016 保留契约）。
func (m *Mapping) Has(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.phToRaw[token]
	return ok
}

func (m *Mapping) Placeholders() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	tokens := make([]string, 0, len(m.phToRaw))
	for token := range m.phToRaw {
		tokens = append(tokens, token)
	}
	return tokens
}

// Clear 清空映射。仅宿主显式丢弃请求状态时使用，正常生命周期由
// 状态表条目整体释放（TD-CPA-016 保留契约）。
func (m *Mapping) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rawToPh = make(map[string]string)
	m.phToRaw = make(map[string]string)
	m.phToEsc = make(map[string]string)
	m.totalBytes = 0
}

func overlapsRanges(ranges [][]int, start, end int) bool {
	for _, item := range ranges {
		if start < item[1] && item[0] < end {
			return true
		}
	}
	return false
}

func jsonEscape(value string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	escaped := string(raw)
	escaped = strings.TrimPrefix(escaped, `"`)
	escaped = strings.TrimSuffix(escaped, `"`)
	return escaped, nil
}
