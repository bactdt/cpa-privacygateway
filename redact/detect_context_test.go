package redact

import (
	"reflect"
	"testing"
)

func TestKeyWords(t *testing.T) {
	cases := map[string][]string{
		"dbPassword":    {"DB", "PASSWORD"},
		"ACCESS_KEY_ID": {"ACCESS", "KEY", "ID"},
		"client-secret": {"CLIENT", "SECRET"},
		"api.key":       {"API", "KEY"},
		"plain":         {"PLAIN"},
	}
	for key, want := range cases {
		if got := keyWords(key); !reflect.DeepEqual(got, want) {
			t.Errorf("keyWords(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{
		"DB_PASSWORD", "apiKey", "ACCESS_KEY_ID", "client-secret",
		"DB_PWD", "TOKEN_TTL", "API_KEY", "private_key", "user_credential",
	}
	for _, key := range sensitive {
		if !IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", key)
		}
	}
	notSensitive := []string{
		"tokenizer", "input_tokens", "max_tokens", "PWD", "OLDPWD", "LOG_FILE",
		"BUILD_ID", "process_id",
	}
	for _, key := range notSensitive {
		if IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", key)
		}
	}
}

func TestSensitiveValueOK(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1234567", false},
		{"12345678", false},
		{"abcdefgh", true},
		{"Summer2024", true},
		{"TRUE", false},
		{"false", false},
		{"Yes", false},
		{"no", false},
		{"[[PG_0123456789abcdef_1]]", false},
		{"[[PG_0123456789abcdef_1]]x", true},
	}
	for _, tc := range cases {
		if got := sensitiveValueOK(tc.value); got != tc.want {
			t.Errorf("sensitiveValueOK(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestScanKV(t *testing.T) {
	text := "DB_PASSWORD=Summer2024abc export API_TOKEN=abc12345xyz\n" +
		`{"client_secret": "s3cr3tValue", "other": 'quoted value'}` + "\n" +
		"plain_key: value12345 and spaced_key = value67890"
	matches := scanKV(text)
	want := []struct {
		key   string
		value string
	}{
		{"DB_PASSWORD", "Summer2024abc"},
		{"API_TOKEN", "abc12345xyz"},
		{"client_secret", "s3cr3tValue"},
		{"other", "quoted value"},
		{"plain_key", "value12345"},
		{"spaced_key", "value67890"},
	}
	if len(matches) != len(want) {
		t.Fatalf("scanKV 匹配数 = %d，want %d：%+v", len(matches), len(want), matches)
	}
	for i, w := range want {
		got := matches[i]
		if got.key != w.key || text[got.valStart:got.valEnd] != w.value {
			t.Errorf("match %d = {%q %q}, want {%q %q}", i, got.key, text[got.valStart:got.valEnd], w.key, w.value)
		}
	}
	for _, m := range matches {
		if m.valStart < 0 || m.valEnd < m.valStart || m.valEnd > len(text) {
			t.Errorf("非法区间：%+v (len=%d)", m, len(text))
		}
	}
}

func TestScanKVQuotedEscape(t *testing.T) {
	text := `{"password":"a\"b12345"}`
	matches := scanKV(text)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if got := text[matches[0].valStart:matches[0].valEnd]; got != `a\"b12345` {
		t.Fatalf("value = %q", got)
	}
}

func TestScanKVEmptyQuotedValue(t *testing.T) {
	if matches := scanKV(`A:""`); len(matches) != 0 {
		t.Fatalf("空引号值不应产生匹配：%+v", matches)
	}
}

func TestScanKVDoesNotCrossLines(t *testing.T) {
	text := "A=1\nB=2"
	matches := scanKV(text)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	for _, m := range matches {
		if m.valEnd > len(text) || m.valStart > m.valEnd {
			t.Fatalf("非法区间：%+v", m)
		}
	}
}

func TestIsSafeIDKey(t *testing.T) {
	cases := []struct {
		key   string
		extra []string
		want  bool
	}{
		{"process_id", nil, true},
		{"PID", nil, true},
		{"trace-id", nil, true},
		{"requestId", nil, true},
		{"db_password", nil, false},
		{"session_id", nil, false},
		{"session_id", []string{"SESSION_ID"}, true},
		{"API_TOKEN", []string{"api_token"}, false},
	}
	for _, tc := range cases {
		if got := isSafeIDKey(tc.key, tc.extra); got != tc.want {
			t.Errorf("isSafeIDKey(%q, %v) = %v, want %v", tc.key, tc.extra, got, tc.want)
		}
	}
}
