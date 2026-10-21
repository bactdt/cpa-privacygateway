package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// panel.html 仍随 zip 分发（file:// 排障用），但不再嵌入二进制。
// 这些断言直接读磁盘内容。
func panelDiskHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("packaging/panel.html")
	if err != nil {
		t.Fatalf("读取 packaging/panel.html 失败：%v", err)
	}
	return string(b)
}

func TestPanelDiskHasNoExternalResources(t *testing.T) {
	html := panelDiskHTML(t)
	re := regexp.MustCompile(`(src|href)\s*=\s*["']https?://`)
	if re.MatchString(html) {
		t.Errorf("panel.html 不应引用外部 http(s) 资源，命中：%v", re.FindAllString(html, -1))
	}
	if strings.Contains(html, "<link ") {
		t.Error("panel.html 不应包含 <link 标签")
	}
	if strings.Contains(html, "<script src") {
		t.Error("panel.html 不应包含 <script src")
	}
}

func TestPanelDiskHasTabStructure(t *testing.T) {
	html := panelDiskHTML(t)
	for _, id := range []string{"tab-conn", "tab-status", "tab-test", "panel-conn", "panel-status", "panel-test"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("panel.html 应包含 Tab 元素 %s", id)
		}
	}
	if !strings.Contains(html, `role="tablist"`) || !strings.Contains(html, `role="tab"`) {
		t.Error("panel.html 应包含 tablist/tab 角色")
	}
}

func TestPanelDiskHasModuleNamespaceAndStatusChecks(t *testing.T) {
	html := panelDiskHTML(t)
	for _, want := range []string{"const Panel", "Panel.conn", "Panel.status", "Panel.test", "function statusChecks"} {
		if !strings.Contains(html, want) {
			t.Errorf("panel.html 应包含 %q（模块化结构）", want)
		}
	}
}
