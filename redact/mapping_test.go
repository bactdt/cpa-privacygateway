package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func newTestMapping(t *testing.T, capacity, maxBytes int) *Mapping {
	t.Helper()
	m, err := NewMapping(capacity, maxBytes)
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	return m
}

func TestNewMappingDefaults(t *testing.T) {
	m := newTestMapping(t, 0, 0)
	if m.capacity != 1024 {
		t.Errorf("capacity default = %d, want 1024", m.capacity)
	}
	if m.maxBytes != 1<<20 {
		t.Errorf("maxBytes default = %d, want %d", m.maxBytes, 1<<20)
	}
	if len(m.nonce) != 16 {
		t.Errorf("nonce length = %d, want 16", len(m.nonce))
	}
	for _, r := range m.nonce {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("nonce %q 含非十六进制字符", m.nonce)
		}
	}
}

func TestMappingPlaceholderFormat(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, err := m.Placeholder("secret")
	if err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	want := "[[PG_" + m.nonce + "_1]]"
	if ph != want {
		t.Errorf("placeholder = %q, want %q", ph, want)
	}
	if !placeholderRe.MatchString(ph) {
		t.Errorf("placeholder %q 不匹配 placeholderRe", ph)
	}
}

func TestMappingPlaceholderReuse(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	first, err := m.Placeholder("same")
	if err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	second, err := m.Placeholder("same")
	if err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	if first != second {
		t.Errorf("同一原文应复用占位符：first=%q second=%q", first, second)
	}
	if m.Size() != 1 {
		t.Errorf("Size = %d, want 1", m.Size())
	}
}

func TestMappingSequential(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	a, _ := m.Placeholder("a")
	b, _ := m.Placeholder("b")
	if a == b {
		t.Errorf("不同原文应得到不同占位符：%q", a)
	}
	if !strings.HasSuffix(a, "_1]]") || !strings.HasSuffix(b, "_2]]") {
		t.Errorf("序号应递增：a=%q b=%q", a, b)
	}
}

func TestMappingCapacityExceeded(t *testing.T) {
	m := newTestMapping(t, 1, 1<<20)
	if _, err := m.Placeholder("first"); err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	if _, err := m.Placeholder("second"); err != ErrCapacity {
		t.Errorf("超容量应返回 ErrCapacity，实际 %v", err)
	}
}

func TestMappingBytesExceeded(t *testing.T) {
	m := newTestMapping(t, 10, 8)
	if _, err := m.Placeholder("12345678"); err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	if _, err := m.Placeholder("9"); err != ErrCapacity {
		t.Errorf("超字节上限应返回 ErrCapacity，实际 %v", err)
	}
}

func TestMappingRestore(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("my-secret")
	if got := m.Restore("token " + ph + " end"); got != "token my-secret end" {
		t.Errorf("Restore = %q", got)
	}
	if got := m.Restore("no placeholder"); got != "no placeholder" {
		t.Errorf("无占位符应原样返回：%q", got)
	}
}

func TestMappingRestoreUnknownKeepsToken(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	other, _ := m.Placeholder("x")
	_ = other
	unknown := "[[PG_0000000000000000_1]]"
	if got := m.Restore(unknown); got != unknown {
		t.Errorf("未知占位符应原样保留：%q", got)
	}
}

func TestMappingRestoreEscaped(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("a\"b\\c")
	got := m.RestoreEscaped(ph)
	if got != `a\"b\\c` {
		t.Errorf("RestoreEscaped = %q, want %q", got, `a\"b\\c`)
	}
}

func TestMappingRestoreEscapedBytes(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("hello")
	got := m.RestoreEscapedBytes([]byte(`"` + ph + `"`))
	if string(got) != `"hello"` {
		t.Errorf("RestoreEscapedBytes = %q", got)
	}
}

func TestMappingRestoreEscapedBytesNoPlaceholder(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	in := []byte(`plain text`)
	got := m.RestoreEscapedBytes(in)
	if string(got) != string(in) {
		t.Errorf("无占位符应原样返回：%q", got)
	}
}

func TestMappingClear(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("x")
	m.Clear()
	if m.Size() != 0 {
		t.Errorf("Clear 后 Size = %d, want 0", m.Size())
	}
	if got := m.Restore(ph); got != ph {
		t.Errorf("Clear 后占位符应无法还原：%q", got)
	}
}

func TestMappingHasAndPlaceholders(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("x")
	if !m.Has(ph) {
		t.Error("Has 应返回 true")
	}
	if m.Has("[[PG_0000000000000000_9]]") {
		t.Error("Has 对未知占位符应返回 false")
	}
	list := m.Placeholders()
	if len(list) != 1 || list[0] != ph {
		t.Errorf("Placeholders = %v, want [%q]", list, ph)
	}
}

func TestRedactNoSensitive(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	got, count, err := m.Redact("hello world", 0, DefaultConfig())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if count != 0 || got != "hello world" {
		t.Errorf("无敏感数据应原样返回：got=%q count=%d", got, count)
	}
}

func TestRedactReplacesCredential(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	got, count, err := m.Redact(`key sk-abcdefghijklmnop1234 here`, 0, DefaultConfig())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if strings.Contains(got, "sk-abcdefghijklmnop1234") {
		t.Errorf("原文未被替换：%q", got)
	}
	if !strings.Contains(got, "[[PG_") {
		t.Errorf("未生成占位符：%q", got)
	}
	restored := m.Restore(got)
	if restored != "key sk-abcdefghijklmnop1234 here" {
		t.Errorf("往返还原失败：%q", restored)
	}
}

func TestRedactSkipsExistingPlaceholder(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("sk-abcdefghijklmnop1234")
	input := "key " + ph + " end"
	got, count, err := m.Redact(input, 0, DefaultConfig())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if count != 0 {
		t.Errorf("已有占位符不应重复替换：count=%d", count)
	}
	if got != input {
		t.Errorf("输出应保持不变：%q", got)
	}
}

func TestRedactBaseOffset(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	got, count, err := m.Redact("xx sk-abcdefghijklmnop1234", 2, DefaultConfig())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if strings.Contains(got, "sk-abcdefghijklmnop1234") {
		t.Errorf("原文未被替换：%q", got)
	}
}

func TestMappingRestoreEscapedBytesDoesNotUnescapeStructure(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	if _, err := m.Placeholder("secret"); err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	in := []byte(`{"delta":{"content":"line1\nline2\ttab"}}`)
	got := m.RestoreEscapedBytes(in)
	if string(got) != string(in) {
		t.Errorf("JSON 结构转义不得被破坏：got %q want %q", got, in)
	}
}

func TestMappingRestoreEscapedBytesBracketEscapeOnly(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	ph, _ := m.Placeholder("secret")
	escaped := strings.ReplaceAll(ph, "[", `\u005b`)
	in := []byte(`{"delta":{"content":"key: ` + escaped + ` end\n"}}`)
	got := m.RestoreEscapedBytes(in)
	if !bytes.Contains(got, []byte("secret")) {
		t.Errorf("转义占位符应还原：%q", got)
	}
	if !bytes.Contains(got, []byte(`\n`)) {
		t.Errorf("无关转义序列必须保留：%q", got)
	}
}

func TestMappingRestoreEscapedBytesStringLiteralPayload(t *testing.T) {
	m := newTestMapping(t, 10, 1<<20)
	token, _ := m.Placeholder(`a"b\c`)
	inner := `key: ` + token
	got := m.RestoreEscapedBytes([]byte(`"` + inner + `"`))
	want, _ := json.Marshal(`key: ` + `a"b\c`)
	if string(got) != string(want) {
		t.Errorf("字符串字面量载荷应保持 JSON 编码：got %s want %s", got, want)
	}
}

func TestMappingConcurrentPlaceholderAndRestore(t *testing.T) {
	m := newTestMapping(t, 1000, 1<<20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ph, err := m.Placeholder(fmt.Sprintf("secret-%d-%d", n, j))
				if err != nil {
					t.Errorf("Placeholder: %v", err)
					return
				}
				_ = m.Restore(ph)
				_ = m.RestoreEscapedBytes([]byte(ph))
				_ = m.Size()
			}
		}(i)
	}
	wg.Wait()
}

func TestMappingRestoreCount(t *testing.T) {
	m := newTestMapping(t, 8, 0)
	ph, err := m.Placeholder("secret-1")
	if err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	unknownPh := formatPlaceholder(m.Nonce(), 99)
	otherPh := formatPlaceholder("0123456789abcdef", 1)
	if m.Nonce() == "0123456789abcdef" {
		t.Skip("nonce collision with fixture")
	}

	cases := []struct {
		name            string
		in, want        string
		restored, unkwn int
	}{
		{"hit", "a " + ph + " b", "a secret-1 b", 1, 0},
		{"unknown seq same nonce", "x " + unknownPh, "x " + unknownPh, 0, 1},
		{"other nonce", "y " + otherPh, "y " + otherPh, 0, 0},
		{"multiple same placeholder", ph + ph + "|" + ph, "secret-1secret-1|secret-1", 3, 0},
		{"mixed", ph + unknownPh + otherPh, "secret-1" + unknownPh + otherPh, 1, 1},
		{"no placeholder", "plain text", "plain text", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, restored, unknown := m.RestoreCount(tc.in)
			if got != tc.want || restored != tc.restored || unknown != tc.unkwn {
				t.Fatalf("RestoreCount(%q) = (%q,%d,%d), want (%q,%d,%d)",
					tc.in, got, restored, unknown, tc.want, tc.restored, tc.unkwn)
			}
			if got != m.Restore(tc.in) {
				t.Fatalf("RestoreCount text differs from Restore for %q", tc.in)
			}
		})
	}

	t.Run("empty mapping", func(t *testing.T) {
		empty := newTestMapping(t, 8, 0)
		own := formatPlaceholder(empty.Nonce(), 1)
		in := "z " + own + " " + otherPh
		got, restored, unknown := empty.RestoreCount(in)
		if got != in || restored != 0 || unknown != 1 {
			t.Fatalf("empty RestoreCount = (%q,%d,%d), want (%q,0,1)", got, restored, unknown, in)
		}
		if got, r, u := empty.RestoreCount("plain"); got != "plain" || r != 0 || u != 0 {
			t.Fatalf("empty plain = (%q,%d,%d)", got, r, u)
		}
	})
}
