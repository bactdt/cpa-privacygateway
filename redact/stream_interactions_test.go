package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func interactionsFrame(v map[string]any) string {
	raw, _ := json.Marshal(v)
	return "event: step.delta\ndata: " + string(raw) + "\n\n"
}

func interactionsTextFrame(text string) string {
	return interactionsFrame(map[string]any{
		"event_type": "step.delta",
		"index":      0,
		"delta":      map[string]any{"type": "text", "text": text},
	})
}

// interactionsTexts 汇总 SSE 输出中 step.delta 的文本字段（text/thought/args）。
func interactionsTexts(t *testing.T, out string) []string {
	t.Helper()
	var texts []string
	for _, payload := range sseDataPayloads(t, out) {
		if payload == "[DONE]" {
			continue
		}
		var v struct {
			EventType string `json:"event_type"`
			Delta     struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
				Arguments string `json:"arguments"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			t.Fatalf("unmarshal %q: %v", payload, err)
		}
		if v.EventType != "step.delta" {
			continue
		}
		switch v.Delta.Type {
		case "text":
			texts = append(texts, v.Delta.Text)
		case "thought_summary":
			texts = append(texts, v.Delta.Content.Text)
		case "arguments_delta":
			texts = append(texts, v.Delta.Arguments)
		}
	}
	return texts
}

func TestInteractionsTextSplit(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	ph := phs[0]
	var got strings.Builder
	for _, piece := range []string{"A " + ph[:7], ph[7:25], ph[25:] + " tail [[PG"} {
		out := effectiveOut(t, s, "interactions", interactionsTextFrame(piece))
		for _, text := range interactionsTexts(t, out) {
			got.WriteString(text)
		}
	}
	stop := "event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"
	out := effectiveOut(t, s, "interactions", stop)
	texts := interactionsTexts(t, out)
	if len(texts) != 1 || texts[0] != "[[PG" {
		t.Fatalf("step.stop 合成帧 = %v\n%s", texts, out)
	}
	got.WriteString(texts[0])
	if got.String() != "A user@example.com tail [[PG" {
		t.Fatalf("got %q", got.String())
	}
	if !strings.HasSuffix(out, stop) {
		t.Fatalf("合成帧应插在 step.stop 之前：%q", out)
	}
	if st := s.Stats(); st.Synthesized != 1 || st.Leftover != 0 || s.Pending() != 0 {
		t.Fatalf("stats = %+v pending=%d", st, s.Pending())
	}
}

func TestInteractionsThoughtAndArguments(t *testing.T) {
	t.Run("thought_summary", func(t *testing.T) {
		s, phs := newPayloadFixture(t, "user@example.com")
		ph := phs[0]
		thoughtFrame := func(text string) string {
			return interactionsFrame(map[string]any{
				"event_type": "step.delta",
				"index":      0,
				"delta": map[string]any{
					"type":    "thought_summary",
					"content": map[string]any{"text": text},
				},
			})
		}
		var got strings.Builder
		for _, piece := range []string{"T " + ph[:9], ph[9:] + " end"} {
			out := effectiveOut(t, s, "interactions", thoughtFrame(piece))
			for _, text := range interactionsTexts(t, out) {
				got.WriteString(text)
			}
		}
		done := "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\"}\n\n"
		out := effectiveOut(t, s, "interactions", done)
		for _, text := range interactionsTexts(t, out) {
			got.WriteString(text)
		}
		if got.String() != "T user@example.com end" {
			t.Fatalf("got %q", got.String())
		}
		if s.Pending() != 0 {
			t.Fatalf("pending = %d", s.Pending())
		}
	})

	t.Run("arguments_delta", func(t *testing.T) {
		s, phs := newPayloadFixture(t, "13812345678")
		ph := phs[0]
		argsFrame := func(text string) string {
			return interactionsFrame(map[string]any{
				"event_type": "step.delta",
				"index":      0,
				"delta":      map[string]any{"type": "arguments_delta", "arguments": text},
			})
		}
		var got strings.Builder
		for _, piece := range []string{"call " + ph[:6], ph[6:] + " end"} {
			out := effectiveOut(t, s, "interactions", argsFrame(piece))
			for _, text := range interactionsTexts(t, out) {
				got.WriteString(text)
			}
		}
		done := "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\"}\n\n"
		out := effectiveOut(t, s, "interactions", done)
		for _, text := range interactionsTexts(t, out) {
			got.WriteString(text)
		}
		if got.String() != "call 13812345678 end" {
			t.Fatalf("got %q", got.String())
		}
		if s.Pending() != 0 {
			t.Fatalf("pending = %d", s.Pending())
		}
	})
}

func TestInteractionsDoneAll(t *testing.T) {
	for _, end := range []string{
		"event: done\ndata: [DONE]\n\n",
		"data: [DONE]\n\n",
		"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\"}\n\n",
	} {
		s, phs := newPayloadFixture(t, "user@example.com")
		piece := "A " + phs[0][:12]
		effectiveOut(t, s, "interactions", interactionsTextFrame(piece))
		out := effectiveOut(t, s, "interactions", end)
		texts := interactionsTexts(t, out)
		if len(texts) != 1 {
			t.Fatalf("end %q 合成帧 = %v\n%s", end, texts, out)
		}
		if !strings.HasSuffix(out, end) {
			t.Fatalf("合成帧应插在结束帧之前：%q", out)
		}
		if s.Pending() != 0 {
			t.Fatalf("end %q pending=%d", end, s.Pending())
		}
	}
}

func TestInteractionsStepStopScoped(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com", "13812345678")
	for i, ph := range phs {
		frame := interactionsFrame(map[string]any{
			"event_type": "step.delta",
			"index":      i,
			"delta":      map[string]any{"type": "text", "text": "x " + ph[:10]},
		})
		effectiveOut(t, s, "interactions", frame)
	}
	stop := "event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"
	out := effectiveOut(t, s, "interactions", stop)
	texts := interactionsTexts(t, out)
	if len(texts) != 1 {
		t.Fatalf("step.stop 只应结束 index 0：%v\n%s", texts, out)
	}
	if s.Pending() == 0 {
		t.Fatalf("index 1 的残留不应被 step.stop 带走")
	}
	done := "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\"}\n\n"
	out = effectiveOut(t, s, "interactions", done)
	if texts := interactionsTexts(t, out); len(texts) != 1 {
		t.Fatalf("interaction.completed 未投递剩余残留：%v", texts)
	}
	if s.Pending() != 0 {
		t.Fatalf("pending = %d", s.Pending())
	}
}

func TestInteractionsNonTargetDelta(t *testing.T) {
	s, phs := newPayloadFixture(t, "user@example.com")
	partial := phs[0][:10]
	for _, typ := range []string{"function_result", "thought_signature"} {
		frame := interactionsFrame(map[string]any{
			"event_type": "step.delta",
			"index":      0,
			"delta":      map[string]any{"type": typ, "text": partial},
		})
		out := effectiveOut(t, s, "interactions", frame)
		if out != frame {
			t.Fatalf("%s 不应作为目标：%q", typ, out)
		}
	}
	if s.Pending() != 0 {
		t.Fatalf("pending = %d", s.Pending())
	}
}
