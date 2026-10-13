package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONEditUnchangedReturnsSameSlice(t *testing.T) {
	in := []byte(`{"a":"x","b":[1,"y"]}`)
	out, changed, err := editStrings(in, func([]string, string) (string, bool) { return "", false })
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if &out[0] != &in[0] || len(out) != len(in) {
		t.Fatalf("expected same slice")
	}
}

func TestJSONEditReplacesOnlyTargetLiteral(t *testing.T) {
	in := []byte(`{ "a" : "keep",  "b":{"c" : "old\u0041", "d":[ "x", "old" ]}, "e":null }`)
	var seen []string
	out, changed, err := editStrings(in, func(path []string, value string) (string, bool) {
		seen = append(seen, strings.Join(path, "\x00")+"="+value)
		if strings.Join(path, "\x00") == "b\x00c" {
			return "new", true
		}
		return "", false
	})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := `{ "a" : "keep",  "b":{"c" : "new", "d":[ "x", "old" ]}, "e":null }`
	if string(out) != want {
		t.Fatalf("got %s\nwant %s", out, want)
	}
	wantSeen := []string{"a=keep", "b\x00c=oldA", "b\x00d\x000=x", "b\x00d\x001=old"}
	if strings.Join(seen, "|") != strings.Join(wantSeen, "|") {
		t.Fatalf("visited %q", seen)
	}
}

func TestJSONEditNestedArraysAndObjects(t *testing.T) {
	in := []byte(`[[{"k":["a",{"z":"b"}]}],"c"]`)
	out, _, err := editStrings(in, func(path []string, value string) (string, bool) {
		if strings.Join(path, "\x00") == "0\x000\x00k\x001\x00z" {
			return value + "!", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `[[{"k":["a",{"z":"b!"}]}],"c"]` {
		t.Fatalf("got %s", out)
	}
}

func TestJSONEditKeysNotVisited(t *testing.T) {
	in := []byte(`{"secret":{"inner":1},"v":"val"}`)
	var values []string
	_, _, err := editStrings(in, func(_ []string, value string) (string, bool) {
		values = append(values, value)
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != "val" {
		t.Fatalf("visited %q", values)
	}
}

func TestJSONEditNoHTMLEscape(t *testing.T) {
	if got := string(encodeTextLiteral(`a<b>&"c"` + "\n")); got != `"a<b>&\"c\"\n"` {
		t.Fatalf("got %s", got)
	}
	out, _, err := editStrings([]byte(`{"t":"x"}`), func([]string, string) (string, bool) { return "<&>", true })
	if err != nil || string(out) != `{"t":"<&>"}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestJSONEditDepthLimit(t *testing.T) {
	deep := []byte(strings.Repeat("[", 65) + `"x"` + strings.Repeat("]", 65))
	if _, _, err := editStrings(deep, func([]string, string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected depth error")
	}
	if _, _, ok := locateContainer(deep, nil); ok {
		t.Fatal("expected locate failure on too-deep JSON")
	}
	shallow := []byte(strings.Repeat("[", 10) + `"x"` + strings.Repeat("]", 10))
	if _, _, err := editStrings(shallow, func([]string, string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONEditInvalidJSON(t *testing.T) {
	for _, in := range []string{``, `{"a":`, `{"a":"b"}x`, `{"a" "b"}`, `[1,]`} {
		if _, _, err := editStrings([]byte(in), func([]string, string) (string, bool) { return "", false }); err == nil {
			t.Fatalf("expected error for %q", in)
		}
		if _, _, ok := locateContainer([]byte(in), nil); ok {
			t.Fatalf("expected locate failure for %q", in)
		}
	}
}

func TestJSONEditLocateAndInsert(t *testing.T) {
	cases := []struct {
		name, body string
		path       []string
		fragment   string
		wantEmpty  bool
		want       string
	}{
		{"empty object", `{"delta":{ }}`, []string{"delta"}, `"content":"x"`, true, `{"delta":{"content":"x" }}`},
		{"empty array", `{"a":[]}`, []string{"a"}, `{"i":0}`, true, `{"a":[{"i":0}]}`},
		{"non-empty object", `{"d":{"role":"r"}}`, []string{"d"}, `"content":"x"`, false, `{"d":{"content":"x","role":"r"}}`},
		{"non-empty array", `{"c":[{"a":1}, 2]}`, []string{"c"}, `{"i":0}`, false, `{"c":[{"i":0},{"a":1}, 2]}`},
		{"root", `{"k":1}`, nil, `"z":2`, false, `{"z":2,"k":1}`},
		{"array index path", `[{"x":{}},{"x":{"y":1}}]`, []string{"1", "x"}, `"n":3`, false, `[{"x":{}},{"x":{"n":3,"y":1}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			open, empty, ok := locateContainer(body, tc.path)
			if !ok || empty != tc.wantEmpty {
				t.Fatalf("ok=%v empty=%v", ok, empty)
			}
			out := insertAt(body, open, []byte(tc.fragment), !empty)
			if string(out) != tc.want || !json.Valid(out) {
				t.Fatalf("got %s want %s", out, tc.want)
			}
			if string(body) != tc.body {
				t.Fatal("insertAt mutated input")
			}
		})
	}
}

func TestJSONEditLocateMissing(t *testing.T) {
	body := []byte(`{"a":"s","b":{}}`)
	for _, path := range [][]string{{"a"}, {"zz"}, {"b", "c"}} {
		if _, _, ok := locateContainer(body, path); ok {
			t.Fatalf("expected miss for %q", path)
		}
	}
}

func TestJSONEditInsertAtBounds(t *testing.T) {
	body := []byte(`ab`)
	if got := insertAt(body, 2, []byte("c"), false); !bytes.Equal(got, []byte("abc")) {
		t.Fatalf("got %s", got)
	}
	if got := insertAt(body, 0, []byte("c"), true); !bytes.Equal(got, []byte("c,ab")) {
		t.Fatalf("got %s", got)
	}
}
