package redact

import (
	"encoding/json"
	"reflect"
	"testing"
)

func targetKeys(plan eventPlan) map[string]string {
	keys := make(map[string]string, len(plan.targets))
	for _, target := range plan.targets {
		keys[target.key] = target.path
	}
	return keys
}

func TestStreamOpenAIMultiChoiceKeys(t *testing.T) {
	event := []byte(`{"id":"x","choices":[` +
		`{"index":1,"delta":{"content":"a","reasoning_content":"r"}},` +
		`{"index":0,"delta":{"reasoning":"s","refusal":"no","role":"assistant"}},` +
		`{"delta":{"content":null}}]}`)
	plan, ok := planOpenAI(event)
	if !ok {
		t.Fatal("planOpenAI ok=false")
	}
	want := map[string]string{
		"c1/content":           joinPath("choices", "0", "delta", "content"),
		"c1/reasoning_content": joinPath("choices", "0", "delta", "reasoning_content"),
		"c0/reasoning":         joinPath("choices", "1", "delta", "reasoning"),
		"c0/refusal":           joinPath("choices", "1", "delta", "refusal"),
	}
	if got := targetKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if len(plan.endPrefixes) != 0 || len(plan.endKeys) != 0 || plan.endAll {
		t.Fatalf("unexpected end signals: %+v", plan)
	}
}

func TestStreamOpenAIToolArgsKeyedByIndex(t *testing.T) {
	event := []byte(`{"choices":[{"index":2,"delta":{"tool_calls":[` +
		`{"index":3,"id":"call","function":{"name":"f","arguments":"{\"a\""}},` +
		`{"function":{"arguments":"x"}},` +
		`{"index":5,"function":{"name":"g"}}]}}]}`)
	plan, ok := planOpenAI(event)
	if !ok {
		t.Fatal("planOpenAI ok=false")
	}
	want := map[string]string{
		"c2/tool3": joinPath("choices", "0", "delta", "tool_calls", "0", "function", "arguments"),
		"c2/tool1": joinPath("choices", "0", "delta", "tool_calls", "1", "function", "arguments"),
	}
	if got := targetKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

func TestStreamOpenAIEmptyDeltaFinishInjects(t *testing.T) {
	event := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	plan, ok := planOpenAI(event)
	if !ok || len(plan.targets) != 0 || !reflect.DeepEqual(plan.endPrefixes, []string{"c0/"}) {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	out, ok := injectOpenAI(event, "c0/content", "[<&")
	if !ok {
		t.Fatal("injectOpenAI ok=false")
	}
	want := `{"choices":[{"index":0,"delta":{"content":"[<&"},"finish_reason":"stop"}]}`
	if string(out) != want {
		t.Fatalf("out = %s, want %s", out, want)
	}

	// 非空 delta 注入：首成员之前加逗号。
	event = []byte(`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"length"}]}`)
	out, ok = injectOpenAI(event, "c0/refusal", "[[PG_")
	if !ok || !json.Valid(out) {
		t.Fatalf("inject non-empty delta: ok=%v out=%s", ok, out)
	}
	want = `{"choices":[{"index":0,"delta":{"refusal":"[[PG_","role":"assistant"},"finish_reason":"length"}]}`
	if string(out) != want {
		t.Fatalf("out = %s, want %s", out, want)
	}
}

func TestStreamOpenAIFinishWithContentNoInjection(t *testing.T) {
	event := []byte(`{"choices":[{"index":0,"delta":{"content":"tail"},"finish_reason":"stop"}]}`)
	plan, ok := planOpenAI(event)
	if !ok {
		t.Fatal("planOpenAI ok=false")
	}
	if _, has := targetKeys(plan)["c0/content"]; !has || !reflect.DeepEqual(plan.endPrefixes, []string{"c0/"}) {
		t.Fatalf("plan = %+v", plan)
	}
	if out, ok := injectOpenAI(event, "c0/content", "["); ok || string(out) != string(event) {
		t.Fatalf("inject into present field should fail: ok=%v out=%s", ok, out)
	}
}

func TestStreamOpenAIUsageChunkNoTargets(t *testing.T) {
	plan, ok := planOpenAI([]byte(`{"choices":[],"usage":{"total_tokens":3}}`))
	if ok || len(plan.targets) != 0 {
		t.Fatalf("usage chunk: plan=%+v ok=%v", plan, ok)
	}
	plan, ok = planOpenAI([]byte(`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	if !ok || len(plan.targets) != 0 || len(plan.endPrefixes) != 0 {
		t.Fatalf("role chunk: plan=%+v ok=%v", plan, ok)
	}
}

func TestStreamOpenAIMissingDeltaInjection(t *testing.T) {
	event := []byte(`{"choices":[{"index":0,"delta":{"content":"x"}},{"index":1,"finish_reason":"tool_calls"}]}`)
	plan, ok := planOpenAI(event)
	if !ok || !reflect.DeepEqual(plan.endPrefixes, []string{"c1/"}) {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	out, ok := injectOpenAI(event, "c1/content", "[")
	want := `{"choices":[{"index":0,"delta":{"content":"x"}},{"delta":{"content":"["},"index":1,"finish_reason":"tool_calls"}]}`
	if !ok || string(out) != want {
		t.Fatalf("inject content: ok=%v out=%s", ok, out)
	}
	out, ok = injectOpenAI(event, "c1/tool2", `{"q":"[`)
	want = `{"choices":[{"index":0,"delta":{"content":"x"}},{"delta":{"tool_calls":[{"index":2,"function":{"arguments":"{\"q\":\"["}}]},"index":1,"finish_reason":"tool_calls"}]}`
	if !ok || string(out) != want {
		t.Fatalf("inject tool without delta: ok=%v out=%s", ok, out)
	}
}

func TestStreamOpenAIToolInjection(t *testing.T) {
	withCalls := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"g"}}]},"finish_reason":"tool_calls"}]}`)
	out, ok := injectOpenAI(withCalls, "c0/tool0", "[")
	want := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"["}},{"index":1,"function":{"name":"g"}}]},"finish_reason":"tool_calls"}]}`
	if !ok || string(out) != want {
		t.Fatalf("inject into tool_calls: ok=%v out=%s", ok, out)
	}
	emptyDelta := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	out, ok = injectOpenAI(emptyDelta, "c0/tool4", "[[")
	want = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":4,"function":{"arguments":"[["}}]},"finish_reason":"tool_calls"}]}`
	if !ok || string(out) != want {
		t.Fatalf("inject tool_calls member: ok=%v out=%s", ok, out)
	}
}

func TestStreamOpenAIInvalid(t *testing.T) {
	for _, event := range []string{`not json`, `[1,2]`, `"str"`, `{"choices":`, `{"id":"x"}`} {
		if _, ok := planOpenAI([]byte(event)); ok {
			t.Fatalf("planOpenAI(%q) ok=true", event)
		}
		if _, ok := injectOpenAI([]byte(event), "c0/content", "["); ok {
			t.Fatalf("injectOpenAI(%q) ok=true", event)
		}
	}
	event := []byte(`{"choices":[{"index":0,"delta":null,"finish_reason":"stop"}]}`)
	for _, key := range []string{"c9/content", "c0/bogus", "x0/content", "c0/toolx"} {
		if _, ok := injectOpenAI([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), key, "["); ok {
			t.Fatalf("injectOpenAI key %q ok=true", key)
		}
	}
	if out, ok := injectOpenAI(event, "c0/content", "["); ok {
		t.Fatalf("null delta should not inject: %s", out)
	}
}
