package redact

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func newCarryFixture(t *testing.T, raws ...string) (*textCarry, []string) {
	t.Helper()
	m := newTestMapping(t, 0, 0)
	phs := make([]string, 0, len(raws))
	for _, raw := range raws {
		ph, err := m.Placeholder(raw)
		if err != nil {
			t.Fatalf("Placeholder: %v", err)
		}
		phs = append(phs, ph)
	}
	return newTextCarry(m), phs
}

// feed 依次投递 pieces（最后一段 final），返回拼接输出并校验每段 UTF-8 合法。
func feed(t *testing.T, tc *textCarry, key string, pieces []string) string {
	t.Helper()
	var b strings.Builder
	for i, piece := range pieces {
		out := tc.restoreText(key, piece, i == len(pieces)-1)
		if !utf8.ValidString(out) {
			t.Fatalf("piece %d output not valid UTF-8: %q", i, out)
		}
		b.WriteString(out)
	}
	return b.String()
}

func splitAt(s string, cuts []int) []string {
	pieces := make([]string, 0, len(cuts)+1)
	prev := 0
	for _, c := range cuts {
		pieces = append(pieces, s[prev:c])
		prev = c
	}
	return append(pieces, s[prev:])
}

func TestTextCarryCompletePlaceholder(t *testing.T) {
	tc, phs := newCarryFixture(t, "user@example.com")
	got := tc.restoreText("c0", "邮箱 "+phs[0]+" 结束", false)
	if got != "邮箱 user@example.com 结束" {
		t.Fatalf("got %q", got)
	}
	if tc.pendingBytes() != 0 || tc.stats.Restored != 1 || tc.stats.Carried != 0 {
		t.Fatalf("pending=%d stats=%+v", tc.pendingBytes(), tc.stats)
	}
}

func TestTextCarrySplitPieces(t *testing.T) {
	tc, phs := newCarryFixture(t, "user@example.com", "13812345678")
	text := "邮箱 " + phs[0] + " 手机" + phs[1] + "结束"
	want := "邮箱 user@example.com 手机13812345678结束"
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 300; iter++ {
		n := 2 + rng.Intn(4) // 2..5 段
		picked := map[int]bool{}
		for len(picked) < n-1 {
			c := 1 + rng.Intn(len(text)-1)
			if utf8.RuneStart(text[c]) {
				picked[c] = true
			}
		}
		cuts := make([]int, 0, n-1)
		for c := 1; c < len(text); c++ {
			if picked[c] {
				cuts = append(cuts, c)
			}
		}
		if got := feed(t, tc, "c0", splitAt(text, cuts)); got != want {
			t.Fatalf("cuts %v: got %q", cuts, got)
		}
		if tc.pendingBytes() != 0 {
			t.Fatalf("pending after final: %d", tc.pendingBytes())
		}
	}
}

func TestTextCarryMaxLengthPlaceholder(t *testing.T) {
	tc, _ := newCarryFixture(t)
	ph := formatPlaceholder(tc.nonce, 123456789)
	if len(ph) != maxPlaceholderLen || maxPlaceholderLen != 33 {
		t.Fatalf("len=%d max=%d", len(ph), maxPlaceholderLen)
	}
	tc.mapping.phToRaw[ph] = "SECRET"
	if out := tc.restoreText("k", "前"+ph[:32], false); out != "前" {
		t.Fatalf("first out %q", out)
	}
	if tc.pendingBytes() != 32 || tc.stats.MaxPending != 32 {
		t.Fatalf("pending=%d stats=%+v", tc.pendingBytes(), tc.stats)
	}
	if out := tc.restoreText("k", ph[32:]+"后", false); out != "SECRET后" {
		t.Fatalf("second out %q", out)
	}
	if tc.stats.MaxPending > maxPlaceholderLen {
		t.Fatalf("MaxPending %d exceeds %d", tc.stats.MaxPending, maxPlaceholderLen)
	}
}

func TestTextCarryFinalFlushes(t *testing.T) {
	tc, phs := newCarryFixture(t, "secret")
	half := phs[0][:10]
	if out := tc.restoreText("k", "a"+half, false); out != "a" {
		t.Fatalf("out %q", out)
	}
	if out := tc.restoreText("k", "", true); out != half {
		t.Fatalf("final out %q", out)
	}
	if tc.pendingBytes() != 0 {
		t.Fatalf("carry not cleared")
	}
}

func TestTextCarryOtherNonceNotHeld(t *testing.T) {
	tc, _ := newCarryFixture(t, "secret")
	other := "[[PG_ffffffffffffffff_1"
	if tc.nonce == "ffffffffffffffff" {
		t.Skip("nonce collision")
	}
	if out := tc.restoreText("k", "x"+other, false); out != "x"+other {
		t.Fatalf("out %q", out)
	}
	if tc.pendingBytes() != 0 || tc.stats.Carried != 0 {
		t.Fatalf("held other nonce: %+v", tc.stats)
	}
}

func TestTextCarryBracketReleased(t *testing.T) {
	tc, _ := newCarryFixture(t, "secret")
	if out := tc.restoreText("k", "数组[", false); out != "数组" {
		t.Fatalf("out %q", out)
	}
	if tc.pendingBytes() != 1 {
		t.Fatalf("pending %d", tc.pendingBytes())
	}
	if out := tc.restoreText("k", "0]", false); out != "[0]" {
		t.Fatalf("release out %q", out)
	}
	if tc.pendingBytes() != 0 {
		t.Fatalf("pending %d", tc.pendingBytes())
	}
}

func TestTextCarryUnknownCompleteNotHeld(t *testing.T) {
	tc, _ := newCarryFixture(t, "secret")
	unknown := formatPlaceholder(tc.nonce, 99)
	if out := tc.restoreText("k", "x"+unknown, false); out != "x"+unknown {
		t.Fatalf("out %q", out)
	}
	if tc.stats.Unknown != 1 || tc.pendingBytes() != 0 {
		t.Fatalf("stats=%+v pending=%d", tc.stats, tc.pendingBytes())
	}
}

func TestTextCarryChineseUTF8(t *testing.T) {
	tc, phs := newCarryFixture(t, "张三")
	text := "你好，" + phs[0] + "，欢迎使用隐私网关。"
	want := "你好，张三，欢迎使用隐私网关。"
	for size := 1; size <= 12; size++ {
		var pieces []string
		for i := 0; i < len(text); {
			j := i + size
			if j > len(text) {
				j = len(text)
			}
			for j < len(text) && !utf8.RuneStart(text[j]) {
				j++
			}
			pieces = append(pieces, text[i:j])
			i = j
		}
		pieces = append(pieces, "")
		if got := feed(t, tc, "c0", pieces); got != want {
			t.Fatalf("size %d: got %q", size, got)
		}
	}
}

func TestTextCarryStats(t *testing.T) {
	tc, phs := newCarryFixture(t, "a@b.cn")
	ph := phs[0]
	tc.restoreText("k1", "x"+ph[:5], false)  // carry 5
	tc.restoreText("k2", "y"+ph[:12], false) // carry 12
	tc.restoreText("k1", ph[5:], false)      // restored
	tc.restoreText("k2", ph[12:], true)      // restored
	want := StreamStats{Restored: 2, Carried: 2, MaxPending: 12}
	if tc.stats != want {
		t.Fatalf("stats=%+v want %+v", tc.stats, want)
	}
}

func TestTextCarryTake(t *testing.T) {
	tc, phs := newCarryFixture(t, "secret")
	half := phs[0][:8]
	for _, key := range []string{"o0/c0/text", "o0/c1/text", "o1/args", "b2"} {
		tc.restoreText(key, half, false)
	}
	if got := tc.takeCarry([]string{"b2", "missing"}, "", false); len(got) != 1 || got["b2"] != half {
		t.Fatalf("keys take %v", got)
	}
	if got := tc.takeCarry(nil, "o0/", false); len(got) != 2 || got["o0/c1/text"] != half {
		t.Fatalf("prefix take %v", got)
	}
	if tc.pendingBytes() != len(half) {
		t.Fatalf("pending %d", tc.pendingBytes())
	}
	if got := tc.takeCarry(nil, "", true); len(got) != 1 || got["o1/args"] != half {
		t.Fatalf("all take %v", got)
	}
	if got := tc.takeCarry(nil, "", true); got != nil {
		t.Fatalf("empty take %v", got)
	}
	tc.restoreText("k", half, false)
	tc.templates = map[string]streamTemplate{"k": {raw: []byte("{}")}}
	before := tc.stats
	tc.reset()
	if tc.pendingBytes() != 0 || tc.templates != nil || tc.stats != before {
		t.Fatalf("reset: pending=%d templates=%v stats=%+v", tc.pendingBytes(), tc.templates, tc.stats)
	}
}

func TestTextCarryNilAndEmptyMapping(t *testing.T) {
	var tc textCarry
	if out := tc.restoreText("k", "a[[PG_", false); out != "a[[PG_" {
		t.Fatalf("nil mapping out %q", out)
	}
	if tc.pendingBytes() != 0 || tc.takeCarry(nil, "", true) != nil {
		t.Fatalf("nil mapping held data")
	}
	empty, _ := newCarryFixture(t)
	if out := empty.restoreText("k", "plain 文本", false); out != "plain 文本" {
		t.Fatalf("empty mapping out %q", out)
	}
	if out := empty.restoreText("k", "", true); out != "" {
		t.Fatalf("empty final %q", out)
	}
}
