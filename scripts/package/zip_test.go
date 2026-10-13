package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// zipTestSynthLib/README/Snippet/Panel 是合成输入，避免测试依赖真实构建产物。
var (
	zipTestSynthLib     = []byte("synthetic-shared-library-bytes")
	zipTestSynthReadme  = []byte("# privacygateway v{{VERSION}}\n")
	zipTestSynthSnippet = []byte("plugins:\n  configs:\n    privacygateway:\n      enabled: true\n")
	zipTestSynthPanel   = []byte("<html>panel</html>\n")
)

func zipTestOpen(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	return r
}

func TestZipLayoutAndModes(t *testing.T) {
	entries := buildEntries("0.1.0", zipTestSynthLib, zipTestSynthReadme, zipTestSynthSnippet, zipTestSynthPanel)
	data, err := buildZip(entries)
	if err != nil {
		t.Fatalf("buildZip: %v", err)
	}
	r := zipTestOpen(t, data)
	if len(r.File) != 5 {
		t.Fatalf("条目数 = %d, 期望 5", len(r.File))
	}
	wantDocs := map[string]bool{
		"privacygateway/README.md":           false,
		"privacygateway/config.snippet.yaml": false,
		"privacygateway/panel.html":          false,
		"privacygateway/checksums.txt":       false,
	}
	sawSO := false
	for _, f := range r.File {
		name := f.Name
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
			t.Errorf("条目名 %q 含不安全片段", name)
		}
		// 官方要求：zip 根目录必须直接包含动态库，文档放 privacygateway/ 子目录。
		if name != "privacygateway-v0.1.0.so" && !strings.HasPrefix(name, "privacygateway/") {
			t.Errorf("条目名 %q 不在允许集合内", name)
		}
		if !f.Mode().IsRegular() {
			t.Errorf("条目 %q 不是常规文件: %v", name, f.Mode())
		}
		if name == "privacygateway-v0.1.0.so" {
			sawSO = true
			if got := f.Mode().Perm(); got != 0o755 {
				t.Errorf(".so 权限 = %o, 期望 755", got)
			}
			continue
		}
		if got := f.Mode().Perm(); got != 0o644 {
			t.Errorf("条目 %q 权限 = %o, 期望 644", name, got)
		}
		if _, ok := wantDocs[name]; ok {
			wantDocs[name] = true
		}
	}
	if !sawSO {
		t.Errorf("缺少根目录 .so 条目 privacygateway-v0.1.0.so")
	}
	for name, seen := range wantDocs {
		if !seen {
			t.Errorf("缺少文档条目 %q", name)
		}
	}
}

func TestZipChecksumsCoverAllButSelf(t *testing.T) {
	entries := buildEntries("0.1.0", zipTestSynthLib, zipTestSynthReadme, zipTestSynthSnippet, zipTestSynthPanel)
	data, err := buildZip(entries)
	if err != nil {
		t.Fatalf("buildZip: %v", err)
	}
	r := zipTestOpen(t, data)
	var checksumData []byte
	byName := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		byName[f.Name] = f
		if f.Name == "privacygateway/checksums.txt" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("打开 checksums.txt: %v", err)
			}
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(rc); err != nil {
				t.Fatalf("读取 checksums.txt: %v", err)
			}
			rc.Close()
			checksumData = buf.Bytes()
		}
	}
	if checksumData == nil {
		t.Fatal("缺少 privacygateway/checksums.txt 条目")
	}
	covered := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimRight(string(checksumData), "\n"), "\n") {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 {
			t.Fatalf("checksums.txt 行格式错误: %q", line)
		}
		sumHex, name := parts[0], parts[1]
		if name == "privacygateway/checksums.txt" {
			t.Fatalf("checksums.txt 不应覆盖自身")
		}
		f, ok := byName[name]
		if !ok {
			t.Fatalf("checksums.txt 覆盖了不存在的条目 %q", name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开条目 %q: %v", name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("读取条目 %q: %v", name, err)
		}
		rc.Close()
		want := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
		if sumHex != want {
			t.Errorf("条目 %q sha256 = %s, 期望 %s", name, sumHex, want)
		}
		if len(sumHex) != 64 {
			t.Errorf("条目 %q 的 sha256 长度 = %d, 期望 64", name, len(sumHex))
		}
		covered[name] = true
	}
	if len(covered) != 4 {
		t.Errorf("checksums.txt 覆盖条目数 = %d, 期望 4", len(covered))
	}
}

func TestZipReproducible(t *testing.T) {
	mk := func() []entry {
		return buildEntries("0.1.0", zipTestSynthLib, zipTestSynthReadme, zipTestSynthSnippet, zipTestSynthPanel)
	}
	first, err := buildZip(mk())
	if err != nil {
		t.Fatalf("buildZip: %v", err)
	}
	second, err := buildZip(mk())
	if err != nil {
		t.Fatalf("buildZip: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("同输入两次 buildZip 输出不一致")
	}
	r := zipTestOpen(t, first)
	for _, f := range r.File {
		if !f.Modified.Equal(fixedTime) {
			t.Errorf("条目 %q 修改时间 = %v, 期望固定时间 %v", f.Name, f.Modified, fixedTime)
		}
	}
	shuffled := mk()
	// 按名称逆序打乱输入条目顺序，buildZip 内部应重新排序。
	sort.Slice(shuffled, func(i, j int) bool { return shuffled[i].name > shuffled[j].name })
	third, err := buildZip(shuffled)
	if err != nil {
		t.Fatalf("buildZip(shuffled): %v", err)
	}
	if !bytes.Equal(first, third) {
		t.Fatalf("打乱输入顺序后 buildZip 输出发生变化")
	}
}

func TestZipRejectsUnsafeNames(t *testing.T) {
	cases := []string{"../evil", "/abs", "a\\b"}
	for _, name := range cases {
		entries := []entry{
			{name, []byte("data"), 0o644},
		}
		if _, err := buildZip(entries); err == nil {
			t.Errorf("buildZip(名称 %q) 应返回 error，实际 nil", name)
		}
	}
}

func TestZipName(t *testing.T) {
	if pluginID != "privacygateway" {
		t.Fatalf("pluginID = %q, 期望 %q", pluginID, "privacygateway")
	}
	got := fmt.Sprintf("%s_%s_%s_%s.zip", pluginID, "0.1.0", targetOS, targetArch)
	want := "privacygateway_0.1.0_linux_amd64.zip"
	if got != want {
		t.Fatalf("zip 命名 = %q, 期望 %q", got, want)
	}
}

func TestZipCheckELF(t *testing.T) {
	if err := checkELF(filepath.Join(t.TempDir(), "does-not-exist.so")); err == nil {
		t.Errorf("checkELF(不存在的路径) 应返回 error")
	}

	notELF := filepath.Join(t.TempDir(), "notelf.so")
	if err := os.WriteFile(notELF, []byte("not elf"), 0o644); err != nil {
		t.Fatalf("写临时文件: %v", err)
	}
	if err := checkELF(notELF); err == nil {
		t.Errorf("checkELF(非 ELF 文件) 应返回 error")
	}

	// 合法 x86-64 ELF 但没有 cliproxy_plugin_init 导出：用 gcc 现编一个最小共享库，
	// 比拿测试二进制更稳（静态链接的测试二进制没有动态符号表，报错文案会不同）。
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		gcc, errLook := exec.LookPath("gcc")
		if errLook != nil {
			t.Log("未找到 gcc，跳过「缺少导出符号」子项（构建 c-shared 本身依赖 gcc，正常环境不会走到这里）")
		} else {
			dir := t.TempDir()
			src := filepath.Join(dir, "noexport.c")
			if err := os.WriteFile(src, []byte("int zip_test_dummy(void) { return 1; }\n"), 0o644); err != nil {
				t.Fatalf("写 C 源文件: %v", err)
			}
			lib := filepath.Join(dir, "noexport.so")
			if out, err := exec.Command(gcc, "-shared", "-fPIC", "-o", lib, src).CombinedOutput(); err != nil {
				t.Fatalf("gcc 编译失败: %v\n%s", err, out)
			}
			err := checkELF(lib)
			if err == nil {
				t.Errorf("checkELF(无 cliproxy_plugin_init 导出的共享库) 应返回 error")
			} else if !strings.Contains(err.Error(), "cliproxy_plugin_init") {
				t.Errorf("错误文本 = %q, 应包含 cliproxy_plugin_init", err.Error())
			}
		}
	}

	const soPath = "../../build/libprivacygateway.so"
	if _, err := os.Stat(soPath); err != nil {
		t.Skip("先执行 make build：../../build/libprivacygateway.so 不存在")
	}
	if err := checkELF(soPath); err != nil {
		t.Errorf("checkELF(%s) = %v, 期望 nil", soPath, err)
	}
}
