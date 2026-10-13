package redact

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStreamResponsesPlanDeltaKeys(t *testing.T) {
	cases := []struct {
		event string
		key   string
	}{
		{`{"type":"response.output_text.delta","output_index":0,"content_index":1,"delta":"hi"}`, "o0/c1/output_text"},
		{`{"type":"response.refusal.delta","output_index":2,"content_index":0,"delta":"no"}`, "o2/c0/refusal"},
		{`{"type":"response.reasoning_text.delta","output_index":1,"content_index":3,"delta":"hm"}`, "o1/c3/reasoning_text"},
		{`{"type":"response.reasoning_summary_text.delta","output_index":1,"content_index":9,"summary_index":2,"delta":"s"}`, "o1/c2/reasoning_summary_text"},
		{`{"type":"response.function_call_arguments.delta","output_index":3,"item_id":"fc","delta":"{\"a\":"}`, "o3/args"},
		{`{"type":"response.custom_tool_call_input.delta","output_index":4,"delta":"x"}`, "o4/custom_input"},
	}
	for _, tc := range cases {
		plan, ok := planResponses([]byte(tc.event))
		if !ok || len(plan.targets) != 1 {
			t.Fatalf("planResponses(%s) = %+v, %v", tc.event, plan, ok)
		}
		if got := plan.targets[0]; got.key != tc.key || got.path != joinPath("delta") {
			t.Fatalf("target = %+v, want key %q", got, tc.key)
		}
		if plan.endAll || len(plan.endKeys) != 0 || len(plan.endPrefixes) != 0 {
			t.Fatalf("unexpected end signals: %+v", plan)
		}
	}
	// delta 非字符串时无目标
	plan, ok := planResponses([]byte(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":5}`))
	if !ok || len(plan.targets) != 0 {
		t.Fatalf("non-string delta plan = %+v, %v", plan, ok)
	}
}

func TestStreamResponsesPlanDoneEndKeys(t *testing.T) {
	cases := []struct {
		event string
		key   string
	}{
		{`{"type":"response.output_text.done","output_index":0,"content_index":1,"text":"full"}`, "o0/c1/output_text"},
		{`{"type":"response.refusal.done","output_index":2,"content_index":0,"refusal":"no"}`, "o2/c0/refusal"},
		{`{"type":"response.reasoning_text.done","output_index":1,"content_index":3,"text":"t"}`, "o1/c3/reasoning_text"},
		{`{"type":"response.reasoning_summary_text.done","output_index":1,"summary_index":2,"text":"s"}`, "o1/c2/reasoning_summary_text"},
		{`{"type":"response.function_call_arguments.done","output_index":3,"arguments":"{}"}`, "o3/args"},
		{`{"type":"response.custom_tool_call_input.done","output_index":4,"input":"x"}`, "o4/custom_input"},
	}
	for _, tc := range cases {
		plan, ok := planResponses([]byte(tc.event))
		if !ok || !reflect.DeepEqual(plan.endKeys, []string{tc.key}) || len(plan.targets) != 0 || plan.endAll {
			t.Fatalf("planResponses(%s) = %+v, %v; want endKeys [%s]", tc.event, plan, ok, tc.key)
		}
	}
}

func TestStreamResponsesPlanPrefixesAndEndAll(t *testing.T) {
	plan, ok := planResponses([]byte(`{"type":"response.content_part.done","output_index":1,"content_index":2,"part":{"type":"output_text","text":"x"}}`))
	if !ok || !reflect.DeepEqual(plan.endPrefixes, []string{"o1/c2/"}) {
		t.Fatalf("content_part.done plan = %+v, %v", plan, ok)
	}
	plan, ok = planResponses([]byte(`{"type":"response.output_item.done","output_index":3,"item":{"type":"message"}}`))
	if !ok || !reflect.DeepEqual(plan.endPrefixes, []string{"o3/"}) {
		t.Fatalf("output_item.done plan = %+v, %v", plan, ok)
	}
	for _, typ := range []string{"response.completed", "response.incomplete", "response.failed"} {
		plan, ok = planResponses([]byte(`{"type":"` + typ + `","response":{"id":"r"}}`))
		if !ok || !plan.endAll || len(plan.targets) != 0 {
			t.Fatalf("%s plan = %+v, %v", typ, plan, ok)
		}
	}
}

func TestStreamResponsesCompletedFullTextNoTargets(t *testing.T) {
	event := `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"邮箱 [[PII_EMAIL_ab_1]]"}]}]}}`
	plan, ok := planResponses([]byte(event))
	if !ok || !plan.endAll || len(plan.targets) != 0 {
		t.Fatalf("completed plan = %+v, %v", plan, ok)
	}
}

func TestStreamResponsesPlanOtherAndInvalid(t *testing.T) {
	plan, ok := planResponses([]byte(`{"type":"response.created","response":{"id":"r"}}`))
	if !ok || !reflect.DeepEqual(plan, eventPlan{}) {
		t.Fatalf("response.created plan = %+v, %v", plan, ok)
	}
	for _, bad := range []string{`not json`, `[1,2]`, `"str"`, ``} {
		if _, ok := planResponses([]byte(bad)); ok {
			t.Fatalf("planResponses(%q) ok = true", bad)
		}
	}
}

func TestStreamResponsesEndByEventName(t *testing.T) {
	cases := []struct {
		name  string
		kinds []string
		all   bool
	}{
		{"response.output_text.done", []string{"output_text"}, false},
		{"response.refusal.done", []string{"refusal"}, false},
		{"response.reasoning_text.done", []string{"reasoning_text"}, false},
		{"response.reasoning_summary_text.done", []string{"reasoning_summary_text"}, false},
		{"response.function_call_arguments.done", []string{"args"}, false},
		{"response.custom_tool_call_input.done", []string{"custom_input"}, false},
		{"response.content_part.done", []string{"output_text", "refusal", "reasoning_text"}, false},
		{"response.output_item.done", nil, true},
		{"response.completed", nil, true},
		{"response.incomplete", nil, true},
		{"response.failed", nil, true},
		{"response.output_text.delta", nil, false},
		{"response.created", nil, false},
	}
	for _, tc := range cases {
		kinds, all := responsesEndByEventName(tc.name)
		if all != tc.all || !reflect.DeepEqual(kinds, tc.kinds) {
			t.Fatalf("responsesEndByEventName(%q) = %v, %v; want %v, %v", tc.name, kinds, all, tc.kinds, tc.all)
		}
	}
	// 返回切片不得与内部表共享
	kinds, _ := responsesEndByEventName("response.content_part.done")
	kinds[0] = "mutated"
	if again, _ := responsesEndByEventName("response.content_part.done"); again[0] != "output_text" {
		t.Fatalf("internal kinds mutated: %v", again)
	}
}

func TestStreamResponsesSynthFrame(t *testing.T) {
	raw := `{"type":"response.output_text.delta","sequence_number":7,"item_id":"m1","output_index":0,"content_index":0,"delta":"[[PII_EM"}`
	carry := "tail \"q\" 中文"
	for _, tc := range []struct{ event, want string }{
		{"", "response.output_text.delta"},
		{"response.output_text.delta", "response.output_text.delta"},
	} {
		out := synthResponses(streamTemplate{raw: []byte(raw), path: []string{"delta"}, event: tc.event}, carry)
		if out == nil {
			t.Fatal("synthResponses returned nil")
		}
		text := string(out)
		if !strings.HasPrefix(text, "event: "+tc.want+"\ndata: ") || !bytes.HasSuffix(out, []byte("\n\n")) {
			t.Fatalf("frame = %q", text)
		}
		body := strings.TrimSuffix(strings.TrimPrefix(text, "event: "+tc.want+"\ndata: "), "\n\n")
		if !json.Valid([]byte(body)) {
			t.Fatalf("invalid JSON body %q", body)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(body), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["delta"] != carry || ev["type"] != "response.output_text.delta" || ev["item_id"] != "m1" {
			t.Fatalf("decoded = %v", ev)
		}
	}
}

func TestStreamResponsesSynthInvalid(t *testing.T) {
	cases := []streamTemplate{
		{},
		{raw: []byte(`{"type":"x","delta":"a"}`)},
		{raw: []byte(`not json`), path: []string{"delta"}},
		{raw: []byte(`{"type":"x","delta":5}`), path: []string{"delta"}},
		{raw: []byte(`{"delta":"a"}`), path: []string{"delta"}}, // 无 event 且无 type
	}
	for i, tpl := range cases {
		if out := synthResponses(tpl, "c"); out != nil {
			t.Fatalf("case %d: synthResponses = %q, want nil", i, out)
		}
	}
}
