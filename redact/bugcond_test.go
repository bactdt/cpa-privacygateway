package redact

// 工具输出占位符泄漏缺陷的探索测试（bugfix tool-output-placeholder-leak，C1–C6）。
// **Validates: Requirements 1.1–1.7, 2.1–2.7**
//
// 这些用例在未修复代码上必须失败；修复后由 3.9 原样重跑并通过。
// 不引用尚未实现的标识符（KindSecretContext / OnStale），以便在未修复代码上
// 得到可读的断言失败而不是整包编译失败。

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

const (
	bugUUID          = "a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b"
	bugEntropyAlpha  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bugSecretContext = Kind("secret_context")
)

// randomBody 生成含数字的确定性随机串，保证高熵检测候选的长度与熵阈值。
func randomBody(rng *rand.Rand, n int) string {
	var b strings.Builder
	b.WriteByte(bugEntropyAlpha[rng.Intn(52)]) // 首字符为字母，保证 \b 边界
	for i := 1; i < n; i++ {
		if i == n/2 {
			b.WriteByte(byte('2' + rng.Intn(8)))
			continue
		}
		b.WriteByte(bugEntropyAlpha[rng.Intn(len(bugEntropyAlpha))])
	}
	return b.String()
}

// spansOfKind 返回指定 Kind 的检出区间文本。
func spansOfKind(text string, spans []span, kind Kind) []string {
	var out []string
	for _, s := range spans {
		if s.kind == kind && s.start >= 0 && s.end <= len(text) && s.start < s.end {
			out = append(out, text[s.start:s.end])
		}
	}
	return out
}

// TestBugCondUUIDNotEntropy：UUID 形式的会话标识不应被高熵检测命中（C1）。
func TestBugCondUUIDNotEntropy(t *testing.T) {
	cfg := DefaultConfig()
	text := "session " + bugUUID + " started"
	spans, _ := cfg.Detect(text, 0, nil)
	if got := spansOfKind(text, spans, KindEntropy); len(got) != 0 {
		t.Fatalf("UUID 被高熵检测命中（C1 复现）：%v，全部检出 %v", got, spans)
	}
}

// TestBugCondFilenameBody：常见扩展名文件名的随机主体不应被高熵检测命中（C2）。
func TestBugCondFilenameBody(t *testing.T) {
	rng := rand.New(rand.NewSource(20261011))
	body := randomBody(rng, 28)
	cfg := DefaultConfig()
	for _, name := range []string{body + ".tar.gz", body + ".tar", body + ".log"} {
		text := "saved " + name + " ok"
		spans, _ := cfg.Detect(text, 0, nil)
		if got := spansOfKind(text, spans, KindEntropy); len(got) != 0 {
			t.Fatalf("文件名主体被高熵检测命中（C2 复现）：%s -> %v", name, got)
		}
	}
}

// TestBugCondSafeIDKey：内置安全键名给出的 id 不应被高熵检测命中（C3）。
func TestBugCondSafeIDKey(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	value := "proc_" + randomBody(rng, 26)
	cfg := DefaultConfig()
	for _, text := range []string{
		"process_id: " + value,
		`"requestId": "` + randomBody(rng, 26) + `"`,
	} {
		spans, _ := cfg.Detect(text, 0, nil)
		if got := spansOfKind(text, spans, KindEntropy); len(got) != 0 {
			t.Fatalf("安全键名的值被高熵检测命中（C3 复现）：%q -> %v", text, got)
		}
	}
}

// TestBugCondSecretContext：敏感键名右侧的短低熵值应被脱敏（C4）。
func TestBugCondSecretContext(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct{ text, value string }{
		{"DB_PASSWORD=Summer2024abc", "Summer2024abc"},
		{"export API_TOKEN=abc12345xyz", "abc12345xyz"},
		{`"client_secret": "s3cr3tValue"`, "s3cr3tValue"},
	}
	for _, tc := range cases {
		spans, _ := cfg.Detect(tc.text, 0, nil)
		var covered bool
		for _, s := range spans {
			if s.kind == bugSecretContext && tc.text[s.start:s.end] == tc.value {
				covered = true
			}
		}
		if !covered {
			t.Fatalf("敏感上下文值未被检出（C4 复现）：%q -> %v", tc.text, spans)
		}
	}
}

// TestBugCondResponsesToolOutput：Responses function_call_output.output 应被扫描（C5）。
func TestBugCondResponsesToolOutput(t *testing.T) {
	secret := "sk-" + strings.Repeat("A", 20)
	bodies := []string{
		`{"input":[{"type":"function_call_output","call_id":"c1","output":"key ` + secret + `"}]}`,
		`{"input":[{"call_id":"c1","output":"key ` + secret + `","type":"function_call_output"}]}`,
	}
	for _, body := range bodies {
		m := newTestMapping(t, 0, 0)
		out, count, err := RedactJSON("openai-response", []byte(body), m, DefaultConfig(), WalkOptions{Format: "openai-response"})
		if err != nil {
			t.Fatalf("RedactJSON: %v", err)
		}
		if count == 0 || strings.Contains(string(out), secret) {
			t.Fatalf("function_call_output.output 未脱敏（C5 复现）：count=%d out=%s", count, out)
		}
	}
}

// TestBugCondStalePlaceholder：请求正文里其他 nonce 的占位符应通过 OnStale 回调（C6）。
func TestBugCondStalePlaceholder(t *testing.T) {
	m := newTestMapping(t, 0, 0)
	body := []byte(`{"content":"old [[PG_ffffffffffffffff_1]] end"}`)
	calls := 0
	var samples []string
	opts, ok := walkOptionsWithStale(&calls, &samples)
	if !ok {
		t.Fatalf("WalkOptions.OnStale 不存在（C6 未修复）")
	}
	if _, _, err := RedactJSON("openai", body, m, DefaultConfig(), opts); err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if calls != 1 {
		t.Fatalf("过期占位符回调次数 = %d，want 1（C6 复现）", calls)
	}
	if len(samples) != 1 || !strings.HasPrefix(samples[0], "[[PG_ffff") || strings.Contains(samples[0], "ffffffffffffffff") {
		t.Fatalf("过期占位符样例未掩码：%v", samples)
	}
}

// walkOptionsWithStale 用反射探测并挂载 WalkOptions.OnStale，
// 使本文件在未实现该字段时仍能编译并给出可读失败。
func walkOptionsWithStale(calls *int, samples *[]string) (WalkOptions, bool) {
	var opts WalkOptions
	field := reflect.ValueOf(&opts).Elem().FieldByName("OnStale")
	if !field.IsValid() {
		return opts, false
	}
	hook := reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
		*calls++
		*samples = append(*samples, args[1].String())
		return nil
	})
	field.Set(hook)
	return opts, true
}

// TestBugCondResponsesOutputJSONValid：修复后的 output 脱敏结果仍是合法 JSON。
func TestBugCondResponsesOutputJSONValid(t *testing.T) {
	secret := "sk-" + strings.Repeat("B", 20)
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"c1","output":"key ` + secret + ` with \"quote\""}]}`)
	m := newTestMapping(t, 0, 0)
	out, _, err := RedactJSON("openai-response", body, m, DefaultConfig(), WalkOptions{Format: "openai-response"})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON：%v\n%s", err, out)
	}
}
