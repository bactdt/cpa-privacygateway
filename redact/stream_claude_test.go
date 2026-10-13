package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamClaudePlanDeltaTargets(t *testing.T) {
	cases := []struct {
		event string
		path  string
		key   string
	}{
		{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`, joinPath("delta", "text"), "b0"},
		{`{"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"hm"}}`, joinPath("delta", "thinking"), "b2"},
		{`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`, joinPath("delta", "partial_json"), "b3"},
	}
	for _, tc := range cases {
		plan, ok := planClaude([]byte(tc.event))
		if !ok || len(plan.targets) != 1 {
			t.Fatalf("planClaude(%s) = %+v, %v", tc.event, plan, ok)
		}
		if got := plan.targets[0]; got.path != tc.path || got.key != tc.key {
			t.Fatalf("target = %+v, want path %q key %q", got, tc.path, tc.key)
		}
		if plan.endAll || len(plan.endKeys) != 0 {
			t.Fatalf("unexpected end signals: %+v", plan)
		}
	}
}

func TestStreamClaudePlanEndSignals(t *testing.T) {
	plan, ok := planClaude([]byte(`{"type":"content_block_stop","index":1}`))
	if !ok || len(plan.endKeys) != 1 || plan.endKeys[0] != "b1" || plan.endAll {
		t.Fatalf("content_block_stop plan = %+v, %v", plan, ok)
	}
	for _, event := range []string{
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	} {
		plan, ok := planClaude([]byte(event))
		if !ok || !plan.endAll || len(plan.targets) != 0 {
			t.Fatalf("planClaude(%s) = %+v, %v", event, plan, ok)
		}
	}
}

func TestStreamClaudePlanOtherAndInvalid(t *testing.T) {
	for _, event := range []string{
		`{"type":"message_start","message":{"content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"ping"}`,
		`{"type":"error","error":{"message":"x"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}`,
	} {
		plan, ok := planClaude([]byte(event))
		if !ok || len(plan.targets) != 0 || plan.endAll || len(plan.endKeys) != 0 {
			t.Fatalf("planClaude(%s) = %+v, %v", event, plan, ok)
		}
	}
	for _, event := range []string{`[1,2]`, `"str"`, `{bad`, ``} {
		if _, ok := planClaude([]byte(event)); ok {
			t.Fatalf("planClaude(%q) ok = true, want false", event)
		}
	}
}

func TestStreamClaudeSynthFrame(t *testing.T) {
	tpl := streamTemplate{
		raw:  []byte(`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"old"}}`),
		path: []string{"delta", "text"},
	}
	carry := "[[PG_a1 <b>&"
	frame := synthClaude(tpl, carry)
	prefix := "event: content_block_delta\ndata: "
	if !bytes.HasPrefix(frame, []byte(prefix)) || !bytes.HasSuffix(frame, []byte("\n\n")) {
		t.Fatalf("frame = %q", frame)
	}
	payload := bytes.TrimSuffix(bytes.TrimPrefix(frame, []byte(prefix)), []byte("\n\n"))
	if !json.Valid(payload) {
		t.Fatalf("invalid payload %s", payload)
	}
	if strings.Contains(string(payload), `\u003c`) || strings.Contains(string(payload), `\u0026`) {
		t.Fatalf("HTML chars escaped: %s", payload)
	}
	var got struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "content_block_delta" || got.Index != 4 || got.Delta.Type != "text_delta" || got.Delta.Text != carry {
		t.Fatalf("decoded = %+v", got)
	}

	tpl.event = "custom"
	if frame := synthClaude(tpl, "x"); !bytes.HasPrefix(frame, []byte("event: custom\ndata: ")) {
		t.Fatalf("custom event frame = %q", frame)
	}
}

func TestStreamClaudeSynthInvalid(t *testing.T) {
	cases := []streamTemplate{
		{},
		{raw: []byte(`{bad`), path: []string{"delta", "text"}},
		{raw: []byte(`{"delta":{"text":"a"}}`)},
		{raw: []byte(`{"delta":{"thinking":"a"}}`), path: []string{"delta", "text"}},
	}
	for _, tpl := range cases {
		if frame := synthClaude(tpl, "x"); frame != nil {
			t.Fatalf("synthClaude(%+v) = %q, want nil", tpl, frame)
		}
	}
}
