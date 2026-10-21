package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"duanjuapp/cpa/redact"
	"gopkg.in/yaml.v3"
)

type detectorConfig struct {
	Credentials   *bool   `yaml:"credentials"`
	Entropy       *bool   `yaml:"entropy"`
	Email         *bool   `yaml:"email"`
	Phone         *bool   `yaml:"phone"`
	IDCard        *bool   `yaml:"id_card"`
	BankCard      *bool   `yaml:"bank_card"`
	MinEntropy    float64 `yaml:"min_entropy"`
	MinEntropyLen int     `yaml:"min_entropy_len"`
}

// panelConfig 控制内嵌检测面板（management 资源路由 panel）的可达性。
// 默认关闭：面板页面会经浏览器持有管理密钥，生产环境建议保持关闭。
type panelConfig struct {
	Enabled bool `yaml:"enabled"`
}

type config struct {
	Enabled             bool           `yaml:"enabled"`
	Priority            int            `yaml:"priority"`
	MaxConcurrency      int            `yaml:"max_concurrency"`
	RejectKeyword       string         `yaml:"reject_keyword"`
	RejectKeywords      []string       `yaml:"reject_keywords"`
	FailClosed          *bool          `yaml:"fail_closed"`
	RequestTTLSeconds   int            `yaml:"request_ttl_seconds"`
	MaxRequests         int            `yaml:"max_requests"`
	MaxBodyBytes        int            `yaml:"max_body_bytes"`
	MaxMappings         int            `yaml:"max_mappings"`
	MaxMappingBytes     int            `yaml:"max_mapping_bytes"`
	RedactToolArguments bool           `yaml:"redact_tool_arguments"`
	MaxFindings         int            `yaml:"max_findings"`
	Allowlist           []string       `yaml:"allowlist"`
	CustomRules         []string       `yaml:"custom_rules"`
	Detectors           detectorConfig `yaml:"detectors"`
	Panel               panelConfig    `yaml:"panel"`
	// RT-1 追踪（TD-5：开箱可见）。默认 true/200/64。
	TraceEnabled     *bool `yaml:"trace_enabled"`
	TraceRecording   *bool `yaml:"trace_recording"`
	TraceMaxRequests int   `yaml:"trace_max_requests"`
	TraceMaxEvents   int   `yaml:"trace_max_events"`
}

func defaultConfig() config {
	closed := true
	traceOn := true
	return config{
		Enabled:           true,
		FailClosed:        &closed,
		RequestTTLSeconds: 600,
		MaxRequests:       8192,
		MaxBodyBytes:      1 << 20,
		MaxMappings:       1024,
		MaxMappingBytes:   1 << 20,
		TraceEnabled:      &traceOn,
		TraceMaxRequests:  200,
		TraceMaxEvents:    64,
	}
}

func parseConfig(raw []byte) (config, error) {
	cfg := defaultConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return config{}, &configParseError{err: err}
		}
	}
	if cfg.RequestTTLSeconds < 1 {
		return config{}, fmt.Errorf("request_ttl_seconds must be greater than zero")
	}
	if cfg.MaxRequests < 1 {
		return config{}, fmt.Errorf("max_requests must be greater than zero")
	}
	if cfg.MaxBodyBytes < 1 {
		return config{}, fmt.Errorf("max_body_bytes must be greater than zero")
	}
	if cfg.MaxMappings < 1 {
		return config{}, fmt.Errorf("max_mappings must be greater than zero")
	}
	if cfg.MaxMappingBytes < 1 {
		return config{}, fmt.Errorf("max_mapping_bytes must be greater than zero")
	}
	if cfg.MaxConcurrency < 0 {
		return config{}, fmt.Errorf("max_concurrency must not be negative")
	}
	if cfg.MaxFindings < 0 {
		return config{}, fmt.Errorf("max_findings must not be negative")
	}
	if cfg.Detectors.MinEntropy < 0 {
		return config{}, fmt.Errorf("detectors.min_entropy must not be negative")
	}
	if cfg.TraceMaxRequests < 1 || cfg.TraceMaxRequests > 2000 {
		return config{}, fmt.Errorf("trace_max_requests must be between 1 and 2000")
	}
	if cfg.TraceMaxEvents < 8 || cfg.TraceMaxEvents > 256 {
		return config{}, fmt.Errorf("trace_max_events must be between 8 and 256")
	}
	for i, pattern := range cfg.CustomRules {
		if _, err := regexp.Compile(pattern); err != nil {
			return config{}, fmt.Errorf("custom_rules[%d] is invalid: %v", i, err)
		}
	}
	return cfg, nil
}

func (c config) failClosed() bool {
	if c.FailClosed == nil {
		return true
	}
	return *c.FailClosed
}

// traceOn 读取追踪开关；nil 视为 true（TD-5：开箱可见）。
func (c config) traceOn() bool {
	return c.TraceEnabled == nil || *c.TraceEnabled
}

// traceRecording 读取记录开关；nil 视为 false（需求 7.6：默认不记录，
// 由页面「开始记录」或配置 trace_recording: true 开启）。
func (c config) traceRecording() bool {
	return c.TraceRecording != nil && *c.TraceRecording
}

// traceLimits 返回经范围钳制后的缓冲容量。
func (c config) traceLimits() (maxRequests, maxEvents int) {
	maxRequests, maxEvents = c.TraceMaxRequests, c.TraceMaxEvents
	if maxRequests < 1 {
		maxRequests = 200
	}
	if maxRequests > 2000 {
		maxRequests = 2000
	}
	if maxEvents < 8 {
		maxEvents = 64
	}
	if maxEvents > 256 {
		maxEvents = 256
	}
	return maxRequests, maxEvents
}

func (c config) ttl() time.Duration {
	return time.Duration(c.RequestTTLSeconds) * time.Second
}

// configParseError 包装 YAML 解析错误（TD-CPA-018）。yaml.v3 的错误会内嵌原始
// 值、自定义标签、锚点名与映射键，直接写入宿主日志可能泄露 keywords/allowlist
// 中的敏感样本。黑名单式"从原串剥值"已被值内反引号、自定义标签、显式 !!int、
// 未知锚点等多次绕过，因此 Error() 改为白名单重建：输出只由固定类别、行号数字、
// 标准 YAML 标签与已校验的 Go 目标类型组成，绝不回显原始错误片段。
// 原始错误仅经 Unwrap 供进程内判定，不得写入日志。
type configParseError struct {
	err error
}

var (
	configEntryLineRe = regexp.MustCompile(`^line (\d+): `)
	configYAMLLineRe  = regexp.MustCompile(`^yaml: line (\d+): `)
	configGoTypeRe    = regexp.MustCompile(`^[A-Za-z0-9_.\[\]*]+$`)
)

// standardYAMLTags 是允许回显的标签；自定义/本地/verbatim 标签可能携带用户内容。
var standardYAMLTags = map[string]bool{
	"!!str": true, "!!int": true, "!!bool": true, "!!float": true, "!!null": true,
	"!!seq": true, "!!map": true, "!!binary": true, "!!timestamp": true, "!!merge": true,
}

// configPlainErrorCategories 把无行号的 yaml 解码错误按固定前缀映射为固定类别。
var configPlainErrorCategories = []struct{ prefix, category string }{
	{"yaml: cannot decode ", "cannot decode tagged value (value redacted)"},
	{"yaml: unknown anchor ", "unknown anchor referenced"},
	{"yaml: anchor ", "recursive anchor"},
	{"yaml: invalid map key", "invalid map key"},
	{"yaml: !!binary value contains invalid base64 data", "invalid base64 in !!binary value"},
	{"yaml: document contains excessive aliasing", "excessive aliasing"},
}

func (e *configParseError) Error() string {
	var typeErr *yaml.TypeError
	if errors.As(e.err, &typeErr) {
		parts := make([]string, 0, len(typeErr.Errors))
		for _, entry := range typeErr.Errors {
			parts = append(parts, sanitizeTypeErrorEntry(entry))
		}
		return "yaml: unmarshal errors: " + strings.Join(parts, "; ")
	}
	if e.err == nil {
		return "config parse error"
	}
	return "yaml: " + sanitizePlainYAMLError(e.err.Error())
}

func (e *configParseError) Unwrap() error {
	return e.err
}

// sanitizeTypeErrorEntry 处理 yaml.TypeError 的单条记录（形如 "line N: ..."）。
func sanitizeTypeErrorEntry(entry string) string {
	prefix := ""
	if m := configEntryLineRe.FindStringSubmatch(entry); m != nil {
		prefix = "line " + m[1] + ": "
		entry = entry[len(m[0]):]
	}
	switch {
	case strings.HasPrefix(entry, "cannot unmarshal "):
		return prefix + describeUnmarshalEntry(strings.TrimPrefix(entry, "cannot unmarshal "))
	case strings.HasPrefix(entry, "mapping key "):
		return prefix + "duplicate mapping key"
	case strings.HasPrefix(entry, "field ") && strings.Contains(entry, " already set "):
		return prefix + "duplicate field"
	case strings.HasPrefix(entry, "field ") && strings.Contains(entry, " not found "):
		return prefix + "unknown field"
	}
	return prefix + "type error"
}

// describeUnmarshalEntry 解析 "<tag>[ `值`] into <GoType>"。Go 类型由 yaml.v3
// 最后拼接，因此取最后一个 " into " 之后的部分，并只在通过字符白名单时回显。
func describeUnmarshalEntry(rest string) string {
	tag := "custom tag"
	if i := strings.IndexByte(rest, ' '); i > 0 && standardYAMLTags[rest[:i]] {
		tag = rest[:i]
	}
	out := "cannot unmarshal " + tag + " value redacted"
	if i := strings.LastIndex(rest, " into "); i >= 0 {
		if goType := rest[i+len(" into "):]; configGoTypeRe.MatchString(goType) {
			out += " into " + goType
		}
	}
	return out
}

// sanitizePlainYAMLError 处理非 TypeError：解析器错误只保留行号，其余按前缀归类。
func sanitizePlainYAMLError(message string) string {
	if m := configYAMLLineRe.FindStringSubmatch(message); m != nil {
		return "line " + m[1] + ": syntax error"
	}
	for _, c := range configPlainErrorCategories {
		if strings.HasPrefix(message, c.prefix) {
			return c.category
		}
	}
	return "config parse error"
}

func (c config) keywords() []string {
	out := make([]string, 0, len(c.RejectKeywords)+1)
	if trimmed := strings.TrimSpace(c.RejectKeyword); trimmed != "" {
		out = append(out, trimmed)
	}
	for _, keyword := range c.RejectKeywords {
		if trimmed := strings.TrimSpace(keyword); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (c config) redactConfig() redact.Config {
	base := redact.DefaultConfig()
	set := func(target *bool, value *bool) {
		if value != nil {
			*target = *value
		}
	}
	set(&base.Credentials, c.Detectors.Credentials)
	set(&base.Entropy, c.Detectors.Entropy)
	set(&base.Email, c.Detectors.Email)
	set(&base.Phone, c.Detectors.Phone)
	set(&base.IDCard, c.Detectors.IDCard)
	set(&base.BankCard, c.Detectors.BankCard)
	if c.Detectors.MinEntropy > 0 {
		base.MinEntropy = c.Detectors.MinEntropy
	}
	if c.Detectors.MinEntropyLen > 0 {
		base.MinEntropyLen = c.Detectors.MinEntropyLen
	}
	if c.MaxFindings > 0 {
		base.MaxFindings = c.MaxFindings
	}
	base.Allowlist = append([]string(nil), c.Allowlist...)
	base.CustomRules = append([]string(nil), c.CustomRules...)
	return base
}
