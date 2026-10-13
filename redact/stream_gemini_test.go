package redact

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStreamGeminiTextAndThoughtKeys(t *testing.T) {
	event := []byte(`{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"think","thought":true},` +
		`{"functionCall":{"name":"f","args":{"q":"v"}}},` +
		`{"text":"answer"}]}}],"usageMetadata":{"promptTokenCount":1}}`)
	plan, ok := planGemini(event)
	if !ok {
		t.Fatal("planGemini ok=false")
	}
	want := []textTarget{
		{path: joinPath("candidates", "0", "content", "parts", "0", "text"), key: "g0/thought"},
		{path: joinPath("candidates", "0", "content", "parts", "2", "text"), key: "g0/text"},
	}
	if !reflect.DeepEqual(plan.targets, want) {
		t.Fatalf("targets = %+v, want %+v", plan.targets, want)
	}
	if len(plan.endPrefixes) != 0 || len(plan.endKeys) != 0 || plan.endAll {
		t.Fatalf("unexpected end signals: %+v", plan)
	}
}

func TestStreamGeminiMultiCandidateIndexAndFinish(t *testing.T) {
	event := []byte(`{"candidates":[` +
		`{"index":2,"content":{"parts":[{"text":"a"}]},"finishReason":"STOP"},` +
		`{"content":{"parts":[{"text":"b","thought":false}]},"finishReason":""},` +
		`{"index":0,"content":{"parts":[{"text":"c"}]}}]}`)
	plan, ok := planGemini(event)
	if !ok {
		t.Fatal("planGemini ok=false")
	}
	want := map[string]string{
		"g2/text": joinPath("candidates", "0", "content", "parts", "0", "text"),
		"g1/text": joinPath("candidates", "1", "content", "parts", "0", "text"),
		"g0/text": joinPath("candidates", "2", "content", "parts", "0", "text"),
	}
	if got := targetKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(plan.endPrefixes, []string{"g2/"}) {
		t.Fatalf("endPrefixes = %v, want [g2/]", plan.endPrefixes)
	}
}

func TestStreamGeminiFunctionCallNotTargeted(t *testing.T) {
	event := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{"text":"x"}}}]},"finishReason":"STOP"}]}`)
	plan, ok := planGemini(event)
	if !ok {
		t.Fatal("planGemini ok=false")
	}
	if len(plan.targets) != 0 {
		t.Fatalf("functionCall must not be targeted: %+v", plan.targets)
	}
	if !reflect.DeepEqual(plan.endPrefixes, []string{"g0/"}) {
		t.Fatalf("endPrefixes = %v", plan.endPrefixes)
	}
}

func TestStreamGeminiInvalidInputs(t *testing.T) {
	for _, event := range []string{
		``, `not json`, `[]`, `"s"`, `{}`, `{"candidates":[]}`, `{"candidates":null}`,
		`{"usageMetadata":{"totalTokenCount":3}}`, `{"candidates":{"0":{}}}`,
	} {
		if plan, ok := planGemini([]byte(event)); ok {
			t.Fatalf("planGemini(%q) ok=true plan=%+v", event, plan)
		}
	}
	for _, tc := range []struct{ event, key string }{
		{`{"candidates":[{"index":0,"content":{"parts":[]}}]}`, "g1/text"},
		{`{"candidates":[{"index":0,"content":{"parts":[]}}]}`, "g0/other"},
		{`{"candidates":[{"index":0,"content":{"parts":[]}}]}`, "c0/text"},
		{`{"candidates":[{"index":0,"content":{"parts":[]}}]}`, "gx/text"},
		{`{"candidates":[{"content":"str"}]}`, "g0/text"},
		{`{"candidates":[{"content":null}]}`, "g0/text"},
		{`{"candidates":[{"content":{"parts":{}}}]}`, "g0/text"},
		{`not json`, "g0/text"},
	} {
		out, ok := injectGemini([]byte(tc.event), tc.key, "x")
		if ok || string(out) != tc.event {
			t.Fatalf("injectGemini(%q,%q) = %q,%v; want unchanged,false", tc.event, tc.key, out, ok)
		}
	}
}

func geminiFirstPart(t *testing.T, out []byte, pos int) map[string]any {
	t.Helper()
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
	var doc struct {
		Candidates []struct {
			Content struct {
				Role  string           `json:"role"`
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Candidates) <= pos || len(doc.Candidates[pos].Content.Parts) == 0 {
		t.Fatalf("unexpected shape: %s (%v)", out, err)
	}
	return doc.Candidates[pos].Content.Parts[0]
}

func TestStreamGeminiInjectExistingParts(t *testing.T) {
	event := []byte(`{"candidates":[{"index":3,"content":{"parts":[]}},{"index":1,"content":{"role":"model","parts":[{"text":"tail"}]},"finishReason":"STOP"}]}`)
	out, ok := injectGemini(event, "g1/text", "carry")
	if !ok {
		t.Fatal("injectGemini ok=false")
	}
	want := `{"candidates":[{"index":3,"content":{"parts":[]}},{"index":1,"content":{"role":"model","parts":[{"text":"carry"},{"text":"tail"}]},"finishReason":"STOP"}]}`
	if string(out) != want {
		t.Fatalf("out = %s\nwant  %s", out, want)
	}
	out, ok = injectGemini(event, "g3/thought", "t")
	if !ok || !reflect.DeepEqual(geminiFirstPart(t, out, 0), map[string]any{"text": "t", "thought": true}) {
		t.Fatalf("thought inject into empty parts = %s,%v", out, ok)
	}
}

func TestStreamGeminiInjectMissingParts(t *testing.T) {
	event := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}`)
	out, ok := injectGemini(event, "g0/thought", "c")
	if !ok {
		t.Fatal("injectGemini ok=false")
	}
	want := `{"candidates":[{"content":{"parts":[{"text":"c","thought":true}],"role":"model"},"finishReason":"STOP"}]}`
	if string(out) != want {
		t.Fatalf("out = %s\nwant  %s", out, want)
	}
	out, ok = injectGemini([]byte(`{"candidates":[{"content":{}}]}`), "g0/text", "c")
	if !ok || string(out) != `{"candidates":[{"content":{"parts":[{"text":"c"}]}}]}` {
		t.Fatalf("inject into empty content = %s,%v", out, ok)
	}
}

func TestStreamGeminiInjectMissingContent(t *testing.T) {
	event := []byte(`{"candidates":[{"finishReason":"STOP","index":0}],"usageMetadata":{"totalTokenCount":5}}`)
	out, ok := injectGemini(event, "g0/text", "c")
	if !ok {
		t.Fatal("injectGemini ok=false")
	}
	want := `{"candidates":[{"content":{"role":"model","parts":[{"text":"c"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"totalTokenCount":5}}`
	if string(out) != want {
		t.Fatalf("out = %s\nwant  %s", out, want)
	}
	out, ok = injectGemini([]byte(`{"candidates":[{}]}`), "g0/text", "c")
	if !ok || string(out) != `{"candidates":[{"content":{"role":"model","parts":[{"text":"c"}]}}]}` {
		t.Fatalf("inject into empty candidate = %s,%v", out, ok)
	}
}

func TestStreamGeminiInjectHTMLUnescaped(t *testing.T) {
	event := []byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`)
	carry := `<a href="u">&</a>` + "\n"
	out, ok := injectGemini(event, "g0/text", carry)
	if !ok {
		t.Fatal("injectGemini ok=false")
	}
	if strings.Contains(string(out), `\u003c`) || strings.Contains(string(out), `\u0026`) || !strings.Contains(string(out), `<a href=\"u\">&</a>\n`) {
		t.Fatalf("HTML chars escaped or wrong: %s", out)
	}
	if got := geminiFirstPart(t, out, 0)["text"]; got != carry {
		t.Fatalf("decoded text = %q, want %q", got, carry)
	}
}
