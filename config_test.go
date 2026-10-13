package main

import (
	"strings"
	"testing"
)

// TestParseConfigErrorNeverEchoesUserContent 覆盖 yaml.v3 会回显用户内容的全部已知
// 错误形态（值、自定义/verbatim 标签、显式标准标签、锚点名、映射键），TD-CPA-018。
func TestParseConfigErrorNeverEchoesUserContent(t *testing.T) {
	const marker = "SECR"
	cases := []struct {
		name, yaml, want string
	}{
		{"backtick in value", "max_requests: \"ab`SECRETXYZ\"\n", "line 1: cannot unmarshal !!str value redacted into int"},
		{"local tag", "max_requests: !SECRETXYZ v\n", "line 1: cannot unmarshal custom tag value redacted into int"},
		{"local tag with value", "max_requests: !foo SECRETXYZ\n", "line 1: cannot unmarshal custom tag value redacted into int"},
		{"uppercase secondary tag", "max_requests: !!SECRETXYZ v\n", "custom tag value redacted"},
		{"verbatim tag", "max_requests: !<tag:SECRETXYZ.com,2000:x> v\n", "custom tag value redacted"},
		{"explicit int tag", "max_requests: !!int SECRETXYZ\n", "yaml: cannot decode tagged value (value redacted)"},
		{"explicit bool tag", "fail_closed: !!bool SECRETXYZ\n", "yaml: cannot decode tagged value (value redacted)"},
		{"unknown anchor", "reject_keywords: *SECRETXYZ\n", "yaml: unknown anchor referenced"},
		{"duplicate nested key", "detectors: {SECRETXYZ: 1, SECRETXYZ: 2}\n", "line 1: duplicate mapping key"},
		{"syntax error", "reject_keywords: [SECRETXYZ\n", "syntax error"},
		{"two segments", "max_requests: [1]\nfail_closed: \"SECRETXYZ\"\n", "line 1: cannot unmarshal !!seq value redacted into int; line 2: cannot unmarshal !!str value redacted into bool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tc.yaml))
			if err == nil {
				t.Fatal("应报错")
			}
			msg := err.Error()
			if strings.Contains(msg, marker) || strings.Contains(msg, "`") {
				t.Errorf("错误信息回显了用户内容：%s", msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("错误信息应包含 %q，实际：%s", tc.want, msg)
			}
		})
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig([]byte(``))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.MaxMappings <= 0 {
		t.Errorf("MaxMappings 应有默认值")
	}
	if cfg.MaxMappingBytes <= 0 {
		t.Errorf("MaxMappingBytes 应有默认值")
	}
	if cfg.MaxRequests <= 0 {
		t.Errorf("MaxRequests 应有默认值")
	}
	if cfg.RedactToolArguments {
		t.Errorf("RedactToolArguments 应默认为 false")
	}
}

func TestParseConfigOverride(t *testing.T) {
	yaml := []byte(`
max_mappings: 100
max_mapping_bytes: 2048
redact_tool_arguments: false
`)
	cfg, err := parseConfig(yaml)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.MaxMappings != 100 {
		t.Errorf("MaxMappings = %d", cfg.MaxMappings)
	}
	if cfg.MaxMappingBytes != 2048 {
		t.Errorf("MaxMappingBytes = %d", cfg.MaxMappingBytes)
	}
	if cfg.RedactToolArguments {
		t.Errorf("RedactToolArguments 应为 false")
	}
}

func TestParseConfigEmptyBytes(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig(nil): %v", err)
	}
	if cfg.MaxMappings <= 0 {
		t.Errorf("nil 输入应返回默认配置")
	}
}

func TestParseConfigInvalidTTL(t *testing.T) {
	yaml := []byte(`request_ttl_seconds: 0`)
	_, err := parseConfig(yaml)
	if err == nil {
		t.Error("TTL=0 应报错")
	}
}

func TestParseConfigFailClosed(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.failClosed() {
		t.Error("FailClosed 默认应为 true")
	}
}

func TestParseConfigPanelDefaultFalse(t *testing.T) {
	cfg, err := parseConfig([]byte(``))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Panel.Enabled {
		t.Error("panel.enabled 默认应为 false")
	}
}

func TestParseConfigPanelEnabledTrue(t *testing.T) {
	cfg, err := parseConfig([]byte("panel:\n  enabled: true\n"))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.Panel.Enabled {
		t.Error("panel.enabled: true 应被解析")
	}
}

func TestParseConfigPanelInvalidType(t *testing.T) {
	_, err := parseConfig([]byte("panel:\n  enabled: \"SECRETXYZ\"\n"))
	if err == nil {
		t.Error("panel.enabled 错类型应报错")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRETXYZ") {
		t.Errorf("错误信息不应回显用户内容：%s", msg)
	}
}

func TestParseConfigKeywords(t *testing.T) {
	cfg := defaultConfig()
	cfg.RejectKeyword = "secret"
	cfg.RejectKeywords = []string{"key", "token"}
	kw := cfg.keywords()
	if len(kw) != 3 {
		t.Errorf("keywords 数量 = %d, want 3", len(kw))
	}
}

func TestParseConfigRedactConfig(t *testing.T) {
	cfg := defaultConfig()
	rc := cfg.redactConfig()
	if !rc.Credentials {
		t.Errorf("redact.Credentials 应默认启用")
	}
	if !rc.Email {
		t.Errorf("redact.Email 应默认启用")
	}
}
