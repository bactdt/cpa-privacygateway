package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func mustMapping(t *testing.T) *Mapping {
	t.Helper()
	return newTestMapping(t, 1024, 1<<20)
}

func TestRedactJSONRoundTrip(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"my email is user@example.com"}],"id":"abc123"}`)
	m := mustMapping(t)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count == 0 {
		t.Fatalf("应检出敏感内容，out=%s", out)
	}
	if bytes.Contains(out, []byte("user@example.com")) {
		t.Errorf("原文未脱敏：%s", out)
	}
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("脱敏后仍应是合法 JSON：%v\n%s", err, out)
	}
	restored, rcount, err := RestoreJSON(out, m)
	if err != nil {
		t.Fatalf("RestoreJSON: %v", err)
	}
	if rcount == 0 {
		t.Errorf("还原应有计数")
	}
	if !bytes.Equal(restored, body) {
		t.Errorf("往返应一致：\n got=%s\nwant=%s", restored, body)
	}
}

func TestRedactJSONStructuralKeysUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-4","role":"assistant","id":"msg_123","text":"hello"}`)
	m := mustMapping(t)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count != 0 {
		t.Errorf("结构键不应被脱敏，count=%d：%s", count, out)
	}
	if !bytes.Contains(out, []byte("gpt-4")) || !bytes.Contains(out, []byte("assistant")) {
		t.Errorf("结构键值被改动：%s", out)
	}
}

func TestRedactJSONOnlyTextKeys(t *testing.T) {
	body := []byte(`{"text":"sk-abcdefghijklmnop1234","secret":"sk-abcdefghijklmnop1234"}`)
	m := mustMapping(t)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count == 0 {
		t.Fatalf("text 键应被脱敏：%s", out)
	}
	if !bytes.Contains(out, []byte(`"text":"[[PG_`)) {
		t.Errorf("text 键未被脱敏：%s", out)
	}
	if !bytes.Contains(out, []byte(`"secret":"sk-abcdefghijklmnop1234"`)) {
		t.Errorf("非 text 键不应被脱敏：%s", out)
	}
}

func TestRedactJSONToolArguments(t *testing.T) {
	body := []byte(`{"tool_calls":[{"function":{"name":"search","arguments":"{\"q\":\"sk-abcdefghijklmnop1234\"}"}}]}`)
	m := mustMapping(t)
	off := WalkOptions{RedactToolArguments: false}
	on := WalkOptions{RedactToolArguments: true}
	outOff, _, err := RedactJSON("openai", body, mustMapping(t), DefaultConfig(), off)
	if err != nil {
		t.Fatalf("RedactJSON off: %v", err)
	}
	outOn, count, err := RedactJSON("openai", body, m, DefaultConfig(), on)
	if err != nil {
		t.Fatalf("RedactJSON on: %v", err)
	}
	if count == 0 {
		t.Errorf("开启工具参数脱敏应有计数：%s", outOn)
	}
	if bytes.Contains(outOn, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("工具参数未脱敏：%s", outOn)
	}
	if !bytes.Contains(outOff, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("关闭时不应脱敏：%s", outOff)
	}
}

func TestRedactJSONMalformed(t *testing.T) {
	m := mustMapping(t)
	_, _, err := RedactJSON("openai", []byte(`{not json`), m, DefaultConfig(), WalkOptions{})
	if err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not valid JSON")) {
		t.Errorf("错误信息应指明 JSON 无效：%v", err)
	}
}

func TestRestoreJSONEmptyMapping(t *testing.T) {
	body := []byte(`{"a":1}`)
	m := mustMapping(t)
	out, count, err := RestoreJSON(body, m)
	if err != nil {
		t.Fatalf("RestoreJSON: %v", err)
	}
	if count != 0 || !bytes.Equal(out, body) {
		t.Errorf("空映射应原样返回：count=%d out=%s", count, out)
	}
}

func TestRedactJSONDepthLimit(t *testing.T) {
	m := mustMapping(t)
	deep := make([]byte, 0)
	for i := 0; i < 40; i++ {
		deep = append(deep, `{"a":`...)
	}
	deep = append(deep, `1`...)
	for i := 0; i < 40; i++ {
		deep = append(deep, []byte("}")...)
	}
	_, _, err := RedactJSON("openai", deep, m, DefaultConfig(), WalkOptions{MaxDepth: 5})
	if err == nil {
		t.Error("超深嵌套应报错")
	}
}

func TestToolArgKeysByFormat(t *testing.T) {
	if !toolArgKeys("claude")["args"] {
		t.Error("claude 应含 args")
	}
	if !toolArgKeys("gemini")["args"] {
		t.Error("gemini 应含 args")
	}
	if !toolArgKeys("openai")["arguments"] {
		t.Error("openai 应含 arguments")
	}
}

func TestApplyReplacementsOrdering(t *testing.T) {
	body := []byte("0123456789")
	reps := []replacement{
		{start: 6, end: 8, value: []byte("B")},
		{start: 1, end: 3, value: []byte("A")},
	}
	got := applyReplacements(body, reps)
	want := "0A345B89"
	if string(got) != want {
		t.Errorf("applyReplacements = %q, want %q", got, want)
	}
}

func TestApplyReplacementsOverlapSkip(t *testing.T) {
	body := []byte("0123456789")
	reps := []replacement{
		{start: 1, end: 4, value: []byte("A")},
		{start: 2, end: 5, value: []byte("B")},
	}
	got := applyReplacements(body, reps)
	if string(got) != "0A456789" {
		t.Errorf("重叠项应跳过：%q", got)
	}
}

func TestRedactJSONCapacityErrorPropagates(t *testing.T) {
	body := []byte(`{"content":"sk-abcdefghijklmnop1234","input":"sk-abcdefghijklmnop5678"}`)
	m := newTestMapping(t, 1, 1<<20)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("容量错误应向上传播，err=%v", err)
	}
	if out != nil || count != 0 {
		t.Errorf("容量错误不应返回部分结果，out=%q count=%d", out, count)
	}
}

func TestRedactJSONCapacityErrorNoPartialRedaction(t *testing.T) {
	body := []byte(`{"content":"sk-abcdefghijklmnop1234","input":"sk-abcdefghijklmnop5678","text":"sk-abcdefghijklmnop9012"}`)
	m := newTestMapping(t, 2, 1<<20)
	out, _, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("容量错误应向上传播，err=%v", err)
	}
	if out != nil {
		t.Errorf("超限后不应返回半份脱敏正文：%s", out)
	}
}

func TestRedactJSONStringArrayUnderTextKey(t *testing.T) {
	body := []byte(`{"input":["sk-abcdefghijklmnop1234","plain text"]}`)
	m := mustMapping(t)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count == 0 {
		t.Fatalf("文本键下的字符串数组应检出敏感内容，out=%s", out)
	}
	if bytes.Contains(out, []byte("sk-abcdefghijklmnop1234")) {
		t.Errorf("数组内敏感值未脱敏：%s", out)
	}
	restored, _, err := RestoreJSON(out, m)
	if err != nil {
		t.Fatalf("RestoreJSON: %v", err)
	}
	if !bytes.Equal(restored, body) {
		t.Errorf("往返应一致：\n got=%s\nwant=%s", restored, body)
	}
}

func TestRedactJSONStringArrayStructuralKeyUntouched(t *testing.T) {
	body := []byte(`{"data":["sk-abcdefghijklmnop1234"],"b64_json":["sk-abcdefghijklmnop5678"]}`)
	m := mustMapping(t)
	out, count, err := RedactJSON("openai", body, m, DefaultConfig(), WalkOptions{})
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if count != 0 {
		t.Errorf("结构键下的字符串数组不应检出，count=%d", count)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("结构键数组应原样保留：%s", out)
	}
}

func TestRedactJSONFindingsErrorPropagates(t *testing.T) {
	m := mustMapping(t)
	cfg := DefaultConfig()
	cfg.MaxFindings = 1
	out, count, err := RedactJSON("openai", []byte(`{"content":"a@b.com c@d.com e@f.com"}`), m, cfg, WalkOptions{Format: "openai"})
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("超限应返回 ErrFindings，实际 %v", err)
	}
	if out != nil {
		t.Errorf("超限时不应输出正文，实际 %q", out)
	}
	if count != 0 {
		t.Errorf("超限时计数应为 0，实际 %d", count)
	}
}

func TestRedactJSONFindingsErrorNoPartialRedaction(t *testing.T) {
	m := mustMapping(t)
	cfg := DefaultConfig()
	cfg.MaxFindings = 1
	// 第一个节点限额内，第二个节点超限：任何部分输出都是漏扫
	// （text/content 均为扫描键；transformJSON 当前把 ErrFindings 包成
	// ErrMalformedJSON 且输出重建正文，本测试锁定整份拒绝语义）
	body := []byte(`{"text":"a@b.com","content":"c@d.com e@f.com"}`)
	out, count, err := RedactJSON("openai", body, m, cfg, WalkOptions{Format: "openai"})
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("第二节点超限应返回 ErrFindings，实际 %v", err)
	}
	if out != nil || count != 0 {
		t.Errorf("不得输出部分脱敏正文：out=%q count=%d", out, count)
	}
}
