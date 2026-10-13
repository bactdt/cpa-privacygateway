package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPanelDefaultAddressLogic(t *testing.T) {
	html := string(panelHTML)
	if !strings.Contains(html, "location.origin") && !strings.Contains(html, "window.location.origin") {
		t.Error("panelHTML 应包含 location.origin（或 window.location.origin）的默认地址逻辑")
	}
	if !strings.Contains(html, "http://127.0.0.1:8317") {
		t.Error("panelHTML 应包含默认地址 http://127.0.0.1:8317")
	}
	if !strings.Contains(html, "location.protocol") {
		t.Error("panelHTML 应包含 location.protocol")
	}
}

func TestPanelHasNoExternalResources(t *testing.T) {
	html := string(panelHTML)
	re := regexp.MustCompile(`(src|href)\s*=\s*["']https?://`)
	if re.MatchString(html) {
		t.Errorf("panelHTML 不应引用外部 http(s) 资源，命中：%v", re.FindAllString(html, -1))
	}
	if strings.Contains(html, "<link ") {
		t.Error("panelHTML 不应包含 <link 标签")
	}
	if strings.Contains(html, "<script src") {
		t.Error("panelHTML 不应包含 <script src")
	}
}

func TestPanelMatchesDisk(t *testing.T) {
	onDisk, err := os.ReadFile("packaging/panel.html")
	if err != nil {
		t.Fatalf("读取 packaging/panel.html 失败：%v", err)
	}
	if !bytes.Equal(onDisk, panelHTML) {
		t.Error("panelHTML 与 packaging/panel.html 字节不一致")
	}
}

func TestPanelHasTabStructure(t *testing.T) {
	html := string(panelHTML)
	for _, id := range []string{"tab-conn", "tab-status", "tab-test", "panel-conn", "panel-status", "panel-test"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("panelHTML 应包含 Tab 元素 %s", id)
		}
	}
	if !strings.Contains(html, `role="tablist"`) || !strings.Contains(html, `role="tab"`) {
		t.Error("panelHTML 应包含 tablist/tab 角色")
	}
}

func TestPanelHasModuleNamespaceAndStatusChecks(t *testing.T) {
	html := string(panelHTML)
	for _, want := range []string{"const Panel", "Panel.conn", "Panel.status", "Panel.test", "function statusChecks"} {
		if !strings.Contains(html, want) {
			t.Errorf("panelHTML 应包含 %q（模块化结构）", want)
		}
	}
}
