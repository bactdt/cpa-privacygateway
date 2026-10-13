package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// snipTestExample 读取仓库根的示例配置（测试运行目录是 scripts/package）。
func snipTestExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("读取 config.example.yaml 失败: %v", err)
	}
	return raw
}

// snipTestTopKeys 解析 YAML，返回顶层 map 的键集合。
func snipTestTopKeys(t *testing.T, data []byte) map[string]struct{} {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("解析 YAML 失败: %v", err)
	}
	keys := make(map[string]struct{}, len(m))
	for k := range m {
		keys[k] = struct{}{}
	}
	return keys
}

// snipTestSnippetMap 取 snippet 中 plugins.configs.privacygateway 这个 map。
func snipTestSnippetMap(t *testing.T, snippet []byte) map[string]any {
	t.Helper()
	var doc struct {
		Plugins struct {
			Configs map[string]map[string]any `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(snippet, &doc); err != nil {
		t.Fatalf("解析 snippet 失败: %v", err)
	}
	inner, ok := doc.Plugins.Configs[pluginID]
	if !ok {
		t.Fatalf("snippet 缺少 plugins.configs.%s", pluginID)
	}
	return inner
}

func TestSnipKeysSuperset(t *testing.T) {
	example := snipTestExample(t)
	snippet := buildSnippet(example)
	got := snipTestSnippetMap(t, snippet)
	for key := range snipTestTopKeys(t, example) {
		if _, ok := got[key]; !ok {
			t.Errorf("snippet 缺少示例配置顶层键 %q", key)
		}
	}
}

func TestSnipValidYAML(t *testing.T) {
	snippet := buildSnippet(snipTestExample(t))
	var top map[string]any
	if err := yaml.Unmarshal(snippet, &top); err != nil {
		t.Fatalf("snippet 不是合法 YAML: %v", err)
	}
	if len(top) != 1 {
		t.Errorf("snippet 顶层应有且仅有 1 个键，实际 %d 个: %v", len(top), top)
	}
	if _, ok := top["plugins"]; !ok {
		t.Errorf("snippet 顶层缺少 plugins 键: %v", top)
	}
}

func TestSnipEnabledFlags(t *testing.T) {
	snippet := buildSnippet(snipTestExample(t))
	var doc struct {
		Plugins struct {
			Enabled bool                      `yaml:"enabled"`
			Configs map[string]map[string]any `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(snippet, &doc); err != nil {
		t.Fatalf("解析 snippet 失败: %v", err)
	}
	if !doc.Plugins.Enabled {
		t.Errorf("plugins.enabled 应为 true")
	}
	inner, ok := doc.Plugins.Configs[pluginID]
	if !ok {
		t.Fatalf("snippet 缺少 plugins.configs.%s", pluginID)
	}
	if v, ok := inner["enabled"].(bool); !ok || !v {
		t.Errorf("plugins.configs.%s.enabled 应为 true，实际 %v", pluginID, inner["enabled"])
	}
}

func TestSnipHeaderMentionsMerge(t *testing.T) {
	snippet := string(buildSnippet(snipTestExample(t)))
	head := snippet
	if idx := strings.Index(snippet, "\nplugins:"); idx >= 0 {
		head = snippet[:idx]
	}
	if !strings.Contains(head, "合并") {
		t.Errorf("snippet 头部注释应说明是合并而非整份替换，头部:\n%s", head)
	}
}

// snipTestReadmeTmpl 读取仓库根的 README 模板。
func snipTestReadmeTmpl(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../packaging/README.md")
	if err != nil {
		t.Fatalf("读取 packaging/README.md 失败: %v", err)
	}
	return raw
}

func TestSnipRenderReadmePlaceholders(t *testing.T) {
	soPath := "privacygateway-v0.1.0.so"
	got := string(renderReadme(snipTestReadmeTmpl(t), "0.1.0", soPath))
	if strings.Contains(got, "{{VERSION}}") {
		t.Errorf("渲染结果仍含 {{VERSION}} 占位符")
	}
	if strings.Contains(got, "{{SO_PATH}}") {
		t.Errorf("渲染结果仍含 {{SO_PATH}} 占位符")
	}
	if !strings.Contains(got, "0.1.0") {
		t.Errorf("渲染结果应含版本 0.1.0")
	}
	if !strings.Contains(got, soPath) {
		t.Errorf("渲染结果应含 so 路径 %s", soPath)
	}
}

func TestSnipReadmeSections(t *testing.T) {
	soPath := "privacygateway-v0.1.0.so"
	got := string(renderReadme(snipTestReadmeTmpl(t), "0.1.0", soPath))
	for _, section := range []string{"前置条件", "安装", "验证", "升级", "卸载", "已知限制"} {
		if !strings.Contains(got, section) {
			t.Errorf("渲染后 README 缺少章节关键词 %q（对应后续任务 9.3 修复 README 模板）", section)
		}
	}
}
