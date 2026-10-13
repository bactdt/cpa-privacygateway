package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func smkMustCreateZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 文件失败：%v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目 %s 失败：%v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写入 zip 条目 %s 失败：%v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip writer 失败：%v", err)
	}
}

func TestSmkEnvelopeParsing(t *testing.T) {
	t.Run("ok 信封解出 result", func(t *testing.T) {
		var env smokeEnvelope
		if err := json.Unmarshal([]byte(`{"ok":true,"result":{"pong":true}}`), &env); err != nil {
			t.Fatalf("解析 ok 信封失败：%v", err)
		}
		if !env.OK {
			t.Fatal("期望 ok=true")
		}
		var result struct {
			Pong bool `json:"pong"`
		}
		if err := json.Unmarshal(env.Result, &result); err != nil {
			t.Fatalf("解析 result 失败：%v", err)
		}
		if !result.Pong {
			t.Fatal("期望 result.pong=true")
		}
	})
	t.Run("error 信封返回 code 且不携带请求内容", func(t *testing.T) {
		var env smokeEnvelope
		body := `{"ok":false,"error":{"code":"x","message":"y"},"result":{"secret":"should-not-leak"}}`
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("解析 error 信封失败：%v", err)
		}
		if env.OK {
			t.Fatal("期望 ok=false")
		}
		if env.Error == nil {
			t.Fatal("期望 error 非空")
		}
		if env.Error.Code != "x" {
			t.Fatalf("期望 code=x，实际 %q", env.Error.Code)
		}
		if env.Error.Message != "y" {
			t.Fatalf("期望 message=y，实际 %q", env.Error.Message)
		}
		if strings.Contains(env.Error.Code, "should-not-leak") || strings.Contains(env.Error.Message, "should-not-leak") {
			t.Fatal("错误信息不应包含请求内容")
		}
	})
	t.Run("非法 JSON 返回 error", func(t *testing.T) {
		var env smokeEnvelope
		if err := json.Unmarshal([]byte(`{not-json`), &env); err == nil {
			t.Fatal("期望解析非法 JSON 返回 error")
		}
	})
}

func TestSmkExtractLibrary(t *testing.T) {
	const entryName = "privacygateway-v0.1.0.so"
	t.Run("合法条目解压成功且权限 0755", func(t *testing.T) {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "privacygateway_v0.1.0_linux_amd64.zip")
		smkMustCreateZip(t, zipPath, map[string]string{entryName: "ELFX"})
		destDir := filepath.Join(dir, "out")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			t.Fatalf("创建 destDir 失败：%v", err)
		}
		libPath, err := extractLibrary(zipPath, destDir)
		if err != nil {
			t.Fatalf("extractLibrary 失败：%v", err)
		}
		if filepath.Base(libPath) != "privacygateway-v0.1.0.so" {
			t.Fatalf("输出文件名不符：%s", libPath)
		}
		info, err := os.Stat(libPath)
		if err != nil {
			t.Fatalf("stat 失败：%v", err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("期望权限 0755，实际 %o", got)
		}
		content, err := os.ReadFile(libPath)
		if err != nil {
			t.Fatalf("读取输出失败：%v", err)
		}
		if string(content) != "ELFX" {
			t.Fatalf("内容不一致：%q", content)
		}
	})
	t.Run("不安全条目名拒绝且无越界写入", func(t *testing.T) {
		for _, name := range []string{"../evil.so", "/abs/x.so", `privacygateway-v\evil.so`} {
			dir := t.TempDir()
			zipPath := filepath.Join(dir, "privacygateway_v0.1.0_linux_amd64.zip")
			smkMustCreateZip(t, zipPath, map[string]string{name: "EVIL"})
			destDir := filepath.Join(dir, "out")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("创建 destDir 失败：%v", err)
			}
			if _, err := extractLibrary(zipPath, destDir); err == nil {
				t.Fatalf("条目 %q 应被拒绝", name)
			}
			entries, err := os.ReadDir(destDir)
			if err != nil {
				t.Fatalf("读取 destDir 失败：%v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("destDir 应为空，实际有 %d 个条目", len(entries))
			}
		}
	})
	t.Run("没有 so 条目报错", func(t *testing.T) {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "privacygateway_v0.1.0_linux_amd64.zip")
		smkMustCreateZip(t, zipPath, map[string]string{"readme.txt": "hi"})
		if _, err := extractLibrary(zipPath, t.TempDir()); err == nil {
			t.Fatal("缺少 .so 条目应返回 error")
		}
	})
}

func TestSmkFindZip(t *testing.T) {
	t.Run("空目录报错", func(t *testing.T) {
		if _, err := findZip(t.TempDir()); err == nil {
			t.Fatal("空目录应返回 error")
		}
	})
	t.Run("恰好一个匹配 zip 返回路径", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "privacygateway_v0.1.0_linux_amd64.zip")
		smkMustCreateZip(t, want, nil)
		got, err := findZip(dir)
		if err != nil {
			t.Fatalf("findZip 失败：%v", err)
		}
		if got != want {
			t.Fatalf("期望 %s，实际 %s", want, got)
		}
	})
	t.Run("两个匹配 zip 报错", func(t *testing.T) {
		dir := t.TempDir()
		smkMustCreateZip(t, filepath.Join(dir, "privacygateway_v0.1.0_linux_amd64.zip"), nil)
		smkMustCreateZip(t, filepath.Join(dir, "privacygateway_v0.2.0_linux_amd64.zip"), nil)
		if _, err := findZip(dir); err == nil {
			t.Fatal("两个匹配 zip 应返回 error")
		}
	})
	t.Run("不匹配模式的 zip 被忽略", func(t *testing.T) {
		dir := t.TempDir()
		smkMustCreateZip(t, filepath.Join(dir, "other.zip"), nil)
		if _, err := findZip(dir); err == nil {
			t.Fatal("不匹配 zip 被忽略后应等同空目录并返回 error")
		}
	})
}

// 官方要求动态库位于 zip 根目录：含 / 的条目（旧布局、穿越路径）一律被
// 根目录过滤跳过，不会走到 validateEntryName；这里的名字必须体现这一点。
func TestSmkExtractLibraryRejectsSubdirectoryEntries(t *testing.T) {
	for _, name := range []string{
		"plugins/linux/amd64/privacygateway-v0.1.0.so",
		"privacygateway-v0.1.0/../x.so",
	} {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "p.zip")
		f, err := os.Create(zipPath)
		if err != nil {
			t.Fatalf("创建 zip: %v", err)
		}
		zw := zip.NewWriter(f)
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("写条目: %v", err)
		}
		_, _ = w.Write([]byte("ELFX"))
		if err := zw.Close(); err != nil {
			t.Fatalf("关闭 zip: %v", err)
		}
		_ = f.Close()

		dest := filepath.Join(dir, "out")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatalf("建目录: %v", err)
		}
		_, errExtract := extractLibrary(zipPath, dest)
		if errExtract == nil {
			t.Errorf("条目 %q 应被拒绝", name)
		} else if !strings.Contains(errExtract.Error(), "zip 根目录没有") {
			t.Errorf("条目 %q 应因不在根目录被过滤（\"zip 根目录没有\"），实际错误：%v", name, errExtract)
		}
		if entries, _ := os.ReadDir(dest); len(entries) != 0 {
			t.Errorf("条目 %q 被拒绝后目标目录不应有文件", name)
		}
	}
}
