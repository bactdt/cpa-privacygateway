package main

import "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

// pluginMetadata 构造注册信封的元数据与面向用户的配置字段说明。
// 从 abi.go 迁出（RT-1 TD-9），保持单文件 ≤ 500 行（T1）。
func pluginMetadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             "privacygateway",
		Version:          pluginVersion,
		Author:           "duanjuapp",
		GitHubRepository: "https://github.com/bactdt/cpa-privacygateway",
		ConfigFields: []pluginapi.ConfigField{
			{Name: "max_concurrency", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum number of intercepted requests allowed in flight; 0 disables the limit."},
			{Name: "reject_keyword", Type: pluginapi.ConfigFieldTypeString, Description: "Terminates requests whose raw body contains this keyword."},
			{Name: "fail_closed", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Reject unparseable or oversized supported requests instead of passing them through."},
			{Name: "max_body_bytes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum request body size inspected by the gateway."},
			{Name: "redact_tool_arguments", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Also redact tool call argument strings."},
			{Name: "allowlist", Type: pluginapi.ConfigFieldTypeArray, Description: "Literal values that are never redacted."},
			{Name: "custom_rules", Type: pluginapi.ConfigFieldTypeArray, Description: "RE2 regular expressions for additional values to redact."},
			{Name: "panel.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Expose the built-in diagnostics panel as a management resource (default false)."},
			{Name: "trace_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Record per-request redaction trace events into an in-memory ring buffer (default true)."},
			{Name: "trace_recording", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Start recording trace events immediately after registration; when false (default) the trace page must be used to start recording."},
			{Name: "trace_max_requests", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum number of request records kept in the trace buffer (default 200, range 1..2000)."},
			{Name: "trace_max_events", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum trace events per request record (default 64, range 8..256)."},
		},
	}
}
