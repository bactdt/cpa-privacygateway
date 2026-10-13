package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerValidVersion(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"semver", "0.1.0", true},
		{"single number", "1", true},
		{"prerelease", "2.0.0-rc.1", true},
		{"build metadata", "1.0.0+build5", true},
		{"empty", "", false},
		{"v prefix short", "v1", false},
		{"v prefix full", "v0.1.0", false},
		{"space inside", "1 2", false},
		{"dots only", "...", false},
		{"leading dot", ".1", false},
		{"negative", "-1", false},
		{"slash", "1/2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validVersion(tc.input); got != tc.want {
				t.Fatalf("validVersion(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestVerReadVersion(t *testing.T) {
	v, err := readVersion("../../plugin.go")
	if err != nil {
		t.Fatalf("readVersion(../../plugin.go) 返回 error：%v", err)
	}
	if v == "" {
		t.Fatal("readVersion(../../plugin.go) 返回空版本")
	}
	if !validVersion(v) {
		t.Fatalf("读到的版本 %q 不是合法版本", v)
	}

	dir := t.TempDir()
	withConst := filepath.Join(dir, "plugin.go")
	if err := os.WriteFile(withConst, []byte("package main\n\nconst pluginVersion = \"9.9.9\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readVersion(withConst)
	if err != nil {
		t.Fatalf("readVersion(%s) 返回 error：%v", withConst, err)
	}
	if got != "9.9.9" {
		t.Fatalf("readVersion = %q, want \"9.9.9\"", got)
	}

	withoutConst := filepath.Join(dir, "noversion.go")
	if err := os.WriteFile(withoutConst, []byte("package main\n\nconst other = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readVersion(withoutConst); err == nil {
		t.Fatalf("readVersion(%s) 应返回 error（缺少 pluginVersion 常量）", withoutConst)
	}
}

func TestVerInferPluginFile(t *testing.T) {
	cases := []struct {
		name        string
		fileName    string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{"versioned so", "privacygateway-v0.1.0.so", "privacygateway", "0.1.0", true},
		{"plain so", "privacygateway.so", "privacygateway", "", true},
		// lib 前缀不能剥掉：宿主按完整文件名推导 ID，libprivacygateway 会被当作 ID 本身，
		// 与注册元数据 Name "privacygateway" 对不上，因此打包时必须去掉 lib 前缀。
		{"lib prefixed so", "libprivacygateway.so", "libprivacygateway", "", true},
		{"empty version", "privacygateway-v.so", "privacygateway-v", "", true},
		{"invalid version", "privacygateway-vabc.so", "privacygateway-vabc", "", true},
		{"dll suffix", "privacygateway.dll", "", "", false},
		{"no suffix", "privacygateway", "", "", false},
		{"double version", "a-v1-v2.so", "a-v1", "2", true},
		{"with path", "plugins/linux/amd64/privacygateway-v0.1.0.so", "privacygateway", "0.1.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, version, ok := inferPluginFile(tc.fileName)
			if ok != tc.wantOK || id != tc.wantID || version != tc.wantVersion {
				t.Fatalf("inferPluginFile(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.fileName, id, version, ok, tc.wantID, tc.wantVersion, tc.wantOK)
			}
		})
	}
	if !validID("privacygateway-vabc") || !validID("privacygateway-v") {
		t.Error("validID 应接受 privacygateway-vabc / privacygateway-v（idPattern 允许连字符结尾，仅靠 validID 不拒绝；inferPluginFile 依赖 validVersion 把它们整体当 id）")
	}
}

func TestVerCheckMetadataName(t *testing.T) {
	if err := checkMetadataName("../../metadata.go"); err != nil {
		t.Fatalf("checkMetadataName(../../metadata.go) = %v, want nil", err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "other.go")
	content := "// Command other\npackage main\n\nvar meta = struct{ Name string }{Name: \"other\"}\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkMetadataName(p)
	if err == nil {
		t.Fatal("checkMetadataName 应返回 error")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("error 信息应包含 \"不一致\"，实际：%v", err)
	}
}

func TestVerRunRejectsOtherPlatform(t *testing.T) {
	for _, tc := range []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"linux", "arm64"},
	} {
		err := run("x", "y", tc.goos, tc.goarch)
		if err == nil {
			t.Fatalf("run(x, y, %s, %s) 应返回 error", tc.goos, tc.goarch)
		}
		if !strings.Contains(err.Error(), "linux/amd64") {
			t.Fatalf("error 应包含 \"linux/amd64\"，实际：%v", err)
		}
	}
}
