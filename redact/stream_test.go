package redact

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestRestoreStreamCrossChunk(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		token  string
		want   string
	}{
		{
			name: "split in middle of token",
			chunks: []string{
				`{"delta":{"content":"Hello [[PG_`,
				"abcdef1234567890_1]] world\"}}\n\n",
			},
			token: "[[PG_abcdef1234567890_1]]",
			want: `{"delta":{"content":"Hello [[PG_abcdef1234567890_1]] world"}}

`,
		},
		{
			name: "split at token boundary",
			chunks: []string{
				`{"delta":{"content":"Token: [[PG_`,
				"abcdef1234567890_1]] then more\"}}\n\n",
			},
			token: "[[PG_abcdef1234567890_1]]",
			want: `{"delta":{"content":"Token: [[PG_abcdef1234567890_1]] then more"}}

`,
		},
		{
			name: "multiple tokens across chunks",
			chunks: []string{
				`{"delta":{"content":"[[PG_`,
				`abcdef1234567890_1]] mid [[PG_`,
				"abcdef1234567890_2]] end\"}}\n\n",
			},
			token: "[[PG_abcdef1234567890_1]][[PG_abcdef1234567890_2]]",
			want: `{"delta":{"content":"[[PG_abcdef1234567890_1]] mid [[PG_abcdef1234567890_2]] end"}}

`,
		},
		{
			name: "no token",
			chunks: []string{
				"{\"delta\":{\"content\":\"plain text\"}}\n\n",
			},
			token: "",
			want:  "{\"delta\":{\"content\":\"plain text\"}}\n\n",
		},
		{
			name: "partial token at end of stream flushed",
			chunks: []string{
				`{"delta":{"content":"partial [[PG_"}`,
			},
			token: "",
			want:  `{"delta":{"content":"partial [[PG_"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMapping(t, 1024, 1<<20)
			if tc.token != "" {
				if strings.Contains(tc.token, "]]") && strings.Count(tc.token, "[[PG_") > 1 {
					for _, tkn := range strings.Split(tc.token, "]]") {
						if tkn != "" {
							m.Placeholder(tkn + "]]")
						}
					}
				} else {
					m.Placeholder(tc.token)
				}
			}
			restorer := NewStreamRestorer(m)
			var buf bytes.Buffer
			for _, ch := range tc.chunks {
				restored, _ := restorer.RestoreChunk([]byte(ch))
				buf.Write(restored)
			}
			buf.Write(restorer.Flush())
			if buf.String() != tc.want {
				t.Errorf("got %q\nwant %q", buf.String(), tc.want)
			}
		})
	}
}

func TestRestoreStreamUnknownTokenPreserved(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	restorer := NewStreamRestorer(m)
	chunk := `{"delta":{"content":"unknown [[PG_zzzzzzzzzzzzzzzz_999]] token"}}\n\n`
	restored, _ := restorer.RestoreChunk([]byte(chunk))
	buf := bytes.Buffer{}
	buf.Write(restored)
	buf.Write(restorer.Flush())
	if !bytes.Contains(buf.Bytes(), []byte("[[PG_zzzzzzzzzzzzzzzz_999]]")) {
		t.Errorf("未知 token 应保留：%s", buf.String())
	}
}

func TestRestoreStreamEscapedToken(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	original := "secret with \"quotes\""
	token, _ := m.Placeholder(original)
	restorer := NewStreamRestorer(m)
	chunk := `{"delta":{"content":"` + strings.ReplaceAll(token, "[", `\u005b`) + `"}}\n\n`
	restored, _ := restorer.RestoreChunk([]byte(chunk))
	buf := bytes.Buffer{}
	buf.Write(restored)
	buf.Write(restorer.Flush())
	// The restored output is JSON, so quotes are escaped
	expected := `secret with \"quotes\"`
	if !bytes.Contains(buf.Bytes(), []byte(expected)) {
		t.Errorf("转义 token 应还原：%s", buf.String())
	}
}

func TestRestoreStreamErrorHandling(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	restorer := NewStreamRestorer(m)
	restored, _ := restorer.RestoreChunk([]byte(""))
	if len(restored) != 0 {
		t.Errorf("空块应返回空：len=%d", len(restored))
	}
	malformed := []byte{0xFF, 0xFE, 0xFD}
	restored, _ = restorer.RestoreChunk(malformed)
	buf := bytes.Buffer{}
	buf.Write(restored)
	buf.Write(restorer.Flush())
}

func TestRestoreStreamMultipleCallsStateful(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("mysecret")
	restorer := NewStreamRestorer(m)
	chunks := []string{
		`{"delta":{"content":"start `,
		token[:len(token)/2],
		token[len(token)/2:] + ` end"}}\n\n`,
	}
	var out strings.Builder
	for _, ch := range chunks {
		restored, _ := restorer.RestoreChunk([]byte(ch))
		out.Write(restored)
	}
	out.WriteString(string(restorer.Flush()))
	if !strings.Contains(out.String(), "mysecret") {
		t.Errorf("跨调用还原失败：%s", out.String())
	}
}

func TestRestoreStreamSSEFormat(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("sk-testkey")
	restorer := NewStreamRestorer(m)
	sse := []string{
		"data: ",
		`{"delta":{"content":"key: `,
		token,
		`"}}\n\n`,
	}
	var out strings.Builder
	for _, ch := range sse {
		restored, _ := restorer.RestoreChunk([]byte(ch))
		out.Write(restored)
	}
	out.WriteString(string(restorer.Flush()))
	if !strings.Contains(out.String(), "sk-testkey") {
		t.Errorf("SSE 格式跨块还原失败：%s", out.String())
	}
}

func TestRestoreStreamFlush(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("secret")
	restorer := NewStreamRestorer(m)
	restorer.RestoreChunk([]byte(`{"content":"` + token[:len(token)/2]))
	flushed := restorer.Flush()
	if !bytes.Contains(flushed, []byte(token[:len(token)/2])) {
		t.Errorf("Flush 应输出未完成的占位符前缀：%s", flushed)
	}
}

func TestRestoreStreamMaxLengthPlaceholderBuffered(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	if _, err := m.Placeholder("warmup"); err != nil {
		t.Fatalf("Placeholder: %v", err)
	}
	restorer := NewStreamRestorer(m)
	placeholder := "[[PG_" + m.nonce + "_123456789]]"
	if len(placeholder) != maxPlaceholderLen {
		t.Fatalf("测试前提错误：占位符长度 %d != maxPlaceholderLen %d", len(placeholder), maxPlaceholderLen)
	}
	restored, drop := restorer.RestoreChunk([]byte(`{"delta":{"content":"` + placeholder))
	if drop {
		t.Errorf("流中间缓冲尾部占位符不应整体丢弃，实际 drop=true")
	}
	if want := `{"delta":{"content":"`; string(restored) != want {
		t.Errorf("最大长度占位符应缓冲尾部，实际输出 %q", restored)
	}
	if flushed := string(restorer.Flush()); flushed != placeholder {
		t.Errorf("Flush 应输出缓冲的占位符，实际 %q", flushed)
	}
}

func TestRestoreStreamSSERestoresDataLinesOnly(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("sk-testkey")
	restorer := NewStreamRestorer(m)
	chunk := []byte(": keep-alive\ndata: {\"delta\":{\"content\":\"key: " + token + "\"}}\n\n")
	out, changed := restorer.RestorePayload("openai", chunk)
	if !changed {
		t.Fatal("含占位符的完整事件应被还原")
	}
	if !bytes.Contains(out, []byte("sk-testkey")) {
		t.Errorf("data 行应还原：%s", out)
	}
	if !bytes.Contains(out, []byte(": keep-alive\n")) {
		t.Errorf("注释行应原样透传：%s", out)
	}
}

func TestRestoreStreamSSEIncompleteLineEmittedImmediately(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("sk-testkey")
	restorer := NewStreamRestorer(m)
	// 无行尾但载荷完整：立即原位还原，不缓冲。
	line := []byte("data: {\"delta\":{\"content\":\"key: " + token + "\"}}")
	out, changed := restorer.RestorePayload("openai", line)
	if !changed || !bytes.Contains(out, []byte("sk-testkey")) || !bytes.HasPrefix(out, []byte("data: ")) {
		t.Errorf("无行尾 data 行应立即还原并保留前缀：out=%q changed=%v", out, changed)
	}
	// 载荷被截断（非合法 JSON）：原样立即输出，不丢弃、不跨块拼接。
	c1 := []byte("data: {\"delta\":{\"content\":\"key: " + token)
	out1, changed1 := restorer.RestorePayload("openai", c1)
	if changed1 || !bytes.Equal(out1, c1) {
		t.Errorf("不完整行应原样立即输出：out=%q changed=%v", out1, changed1)
	}
	c2 := []byte("\"}}\n\n")
	out2, changed2 := restorer.RestorePayload("openai", c2)
	if changed2 || !bytes.Equal(out2, c2) {
		t.Errorf("后续块不得与前块拼接：out=%q changed=%v", out2, changed2)
	}
	if restorer.Pending() != 0 {
		t.Errorf("不应跨块缓冲行，Pending=%d", restorer.Pending())
	}
}

func TestRestoreStreamSSEPlaceholderSplitAcrossEventsNotMerged(t *testing.T) {
	m := newTestMapping(t, 1024, 1<<20)
	token, _ := m.Placeholder("sk-testkey")
	restorer := NewStreamRestorer(m)
	split := len(token) / 2
	event := func(index int, content string) []byte {
		return []byte(`{"choices":[{"index":` + strconv.Itoa(index) + `,"delta":{"content":"` + content + `"}}]}`)
	}
	contentOf := func(out []byte) string {
		var v struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &v); err != nil || len(v.Choices) != 1 {
			t.Fatalf("输出不是合法单 choice 事件：%s err=%v", out, err)
		}
		return v.Choices[0].Delta.Content
	}
	out1, _ := restorer.RestorePayload("openai", event(0, "a "+token[:split]))
	// 另一个 choice 的文本流以占位符后半开头：不得与 choice 0 的暂扣合并。
	other, _ := restorer.RestorePayload("openai", event(1, token[split:]+" x"))
	if got := contentOf(other); got != token[split:]+" x" {
		t.Errorf("不同 choice 不得合并还原：%q", got)
	}
	out2, _ := restorer.RestorePayload("openai", event(0, token[split:]+" b"))
	if got := contentOf(out1) + contentOf(out2); got != "a sk-testkey b" {
		t.Errorf("同一文本流跨事件占位符应还原：%q", got)
	}
}

func TestStreamRestorerStatsAndReset(t *testing.T) {
	m, err := NewMapping(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStreamRestorer(m)
	partial := "[[PG_" + m.Nonce()[:4]

	hold := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := s.text.restoreText("choice0", "hi "+partial, false)
		if out != "hi " {
			t.Fatalf("restoreText 应暂扣占位符前缀，得到 %q", out)
		}
		return s.text.pendingBytes()
	}

	held := hold()
	if held != len(partial) {
		t.Fatalf("暂扣字节 = %d，期望 %d", held, len(partial))
	}
	if got := s.Pending(); got != held {
		t.Fatalf("Pending = %d，期望包含暂扣 %d", got, held)
	}
	before := s.Stats()
	if before.Carried != 1 || before.MaxPending != held {
		t.Fatalf("统计异常：%+v", before)
	}

	if out := s.Flush(); len(out) != 0 {
		t.Fatalf("Flush 返回值应仍为字节残留（空），得到 %q", out)
	}
	after := s.Stats()
	if after.Leftover-before.Leftover != held {
		t.Fatalf("Leftover 增量 = %d，期望 %d", after.Leftover-before.Leftover, held)
	}
	if s.Pending() != 0 {
		t.Fatalf("Flush 后应清空暂扣，Pending = %d", s.Pending())
	}

	hold()
	s.Reset()
	if s.Pending() != 0 {
		t.Fatalf("Reset 后应清空暂扣，Pending = %d", s.Pending())
	}
	if s.Stats().Leftover != after.Leftover {
		t.Fatalf("Reset 不应计入 Leftover 也不应清零统计")
	}

	copied := s.Stats()
	copied.Leftover = 9999
	copied.Restored = 9999
	if got := s.Stats(); got.Leftover == 9999 || got.Restored == 9999 {
		t.Fatalf("Stats 应返回副本，内部被修改：%+v", got)
	}

	nilRestorer := NewStreamRestorer(nil)
	if nilRestorer.Pending() != 0 {
		t.Fatalf("nil mapping Pending 应为 0")
	}
	_ = nilRestorer.Stats()
	if out := nilRestorer.Flush(); len(out) != 0 {
		t.Fatalf("nil mapping Flush 应为空")
	}
	nilRestorer.Reset()
	nilRestorer.mu.Lock()
	if out := nilRestorer.text.restoreText("k", "x [[PG_", false); out != "x [[PG_" {
		t.Errorf("nil mapping 不应暂扣，得到 %q", out)
	}
	nilRestorer.mu.Unlock()
}
