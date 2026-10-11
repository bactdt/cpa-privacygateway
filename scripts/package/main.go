// Command package 把 build/libprivacygateway.so 打成 CPA 可直接解压使用的 zip 插件包。
//
// 包结构（解压到 CPA 工作目录）：
//
//	plugins/linux/amd64/privacygateway-v<ver>.so
//	privacygateway/README.md
//	privacygateway/config.snippet.yaml
//	privacygateway/checksums.txt
//
// 宿主按文件名推导插件 ID，因此库文件必须从 libprivacygateway.so 重命名为
// privacygateway-v<ver>.so。设计见 .kiro/specs/plugin-package/design.md。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	pluginID   = "privacygateway"
	targetOS   = "linux"
	targetArch = "amd64"
)

var (
	// 与宿主 pluginVersionPattern 一致：不得以 v 开头。
	versionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
	// 与宿主 pluginIDPattern 一致。
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	metadataName = regexp.MustCompile(`Name:\s+"` + pluginID + `"`)
	// 固定时间戳，保证同输入产出字节级一致的 zip。
	fixedTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
)

type entry struct {
	name string
	data []byte
	mode os.FileMode
}

func main() {
	lib := flag.String("lib", "build/libprivacygateway.so", "输入共享库")
	out := flag.String("out", ".", "zip 输出目录（默认当前目录）")
	goos := flag.String("goos", targetOS, "目标系统（仅支持 linux）")
	goarch := flag.String("goarch", targetArch, "目标架构（仅支持 amd64）")
	flag.Parse()

	if err := run(*lib, *out, *goos, *goarch); err != nil {
		fmt.Fprintln(os.Stderr, "package: "+err.Error())
		os.Exit(1)
	}
}

func run(lib, out, goos, goarch string) error {
	if goos != targetOS || goarch != targetArch {
		return fmt.Errorf("仅支持 %s/%s（决策 PK-1），收到 %s/%s", targetOS, targetArch, goos, goarch)
	}
	version, err := readVersion("plugin.go")
	if err != nil {
		return err
	}
	if !validVersion(version) {
		return fmt.Errorf("版本 %q 不符合宿主规则 ^[0-9][0-9A-Za-z.+-]*$", version)
	}
	if err := checkMetadataName("abi.go"); err != nil {
		return err
	}
	libData, err := os.ReadFile(lib)
	if err != nil {
		return fmt.Errorf("读取共享库失败（先执行 make build）：%w", err)
	}
	if err := checkELF(lib); err != nil {
		return err
	}
	example, err := os.ReadFile("config.example.yaml")
	if err != nil {
		return err
	}
	readme, err := os.ReadFile("packaging/README.md")
	if err != nil {
		return err
	}
	panel, err := os.ReadFile("packaging/panel.html")
	if err != nil {
		return err
	}

	soName := fmt.Sprintf("%s-v%s.so", pluginID, version)
	soPath := fmt.Sprintf("plugins/%s/%s/%s", targetOS, targetArch, soName)
	if id, v, _ := inferPluginFile(soName); id != pluginID || v != version {
		return fmt.Errorf("文件名 %s 推导出 id=%q version=%q，与预期 id=%q version=%q 不一致（防回归）", soName, id, v, pluginID, version)
	}
	snippet := buildSnippet(example)
	entries := buildEntries(version, libData, renderReadme(readme, version, soPath), snippet, panel)

	zipName := fmt.Sprintf("%s_%s_%s_%s.zip", pluginID, version, targetOS, targetArch)
	zipPath := filepath.Join(out, zipName)
	data, err := buildZip(entries)
	if err != nil {
		return err
	}
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	fmt.Printf("package: 已生成 %s（%d 字节，sha256=%x）\n", zipPath, len(data), sum)
	return nil
}

// validVersion 与宿主 validPluginVersion 一致：非空、不得以 v 开头、匹配 versionPattern。
func validVersion(v string) bool {
	return v != "" && !strings.HasPrefix(v, "v") && versionPattern.MatchString(v)
}

// validID 与宿主 validPluginID 一致。
func validID(id string) bool {
	return idPattern.MatchString(id)
}

// inferPluginFile 复刻宿主 pluginFileFromPath 对 .so 文件名的 ID/版本推导规则，
// 打包时用于防回归：保证 zip 内库名能被宿主推导出预期 ID 与版本。
func inferPluginFile(fileName string) (id, version string, ok bool) {
	base := filepath.Base(fileName)
	if !strings.HasSuffix(strings.ToLower(base), ".so") {
		return "", "", false
	}
	name := base[:len(base)-len(".so")]
	id = name
	version = ""
	if versionIndex := strings.LastIndex(name, "-v"); versionIndex > 0 {
		candidateID := name[:versionIndex]
		candidateVersion := name[versionIndex+2:]
		if validID(candidateID) && validVersion(candidateVersion) {
			id = candidateID
			version = candidateVersion
		}
	}
	if !validID(id) {
		return "", "", false
	}
	return id, version, true
}

// renderReadme 把 README 模板中的 {{VERSION}} 与 {{SO_PATH}} 占位符替换为实际值。
func renderReadme(tmpl []byte, version, soPath string) []byte {
	data := bytes.ReplaceAll(tmpl, []byte("{{VERSION}}"), []byte(version))
	return bytes.ReplaceAll(data, []byte("{{SO_PATH}}"), []byte(soPath))
}

// buildEntries 组装 zip 的全部条目：.so 0755，其余 0644，最后是覆盖前四项的 checksums.txt。
func buildEntries(version string, libData, readme, snippet, panel []byte) []entry {
	soName := fmt.Sprintf("%s-v%s.so", pluginID, version)
	soPath := fmt.Sprintf("plugins/%s/%s/%s", targetOS, targetArch, soName)
	docDir := pluginID + "/"
	entries := []entry{
		{soPath, libData, 0o755},
		{docDir + "README.md", readme, 0o644},
		{docDir + "config.snippet.yaml", snippet, 0o644},
		{docDir + "panel.html", panel, 0o644},
	}
	entries = append(entries, entry{docDir + "checksums.txt", checksums(entries), 0o644})
	return entries
}

// readVersion 用 go/parser 读取 plugin.go 中的 pluginVersion 常量（单一版本源）。
func readVersion(path string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return "", err
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 || vs.Names[0].Name != "pluginVersion" || len(vs.Values) == 0 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				break
			}
			return strconv.Unquote(lit.Value)
		}
	}
	return "", fmt.Errorf("%s 中未找到字符串常量 pluginVersion", path)
}

// checkMetadataName 断言注册元数据名称等于文件名推导出的插件 ID。
func checkMetadataName(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !metadataName.Match(raw) {
		return fmt.Errorf("%s 的 Metadata.Name 与插件 ID %q 不一致", path, pluginID)
	}
	return nil
}

// checkELF 确认输入是 x86-64 ELF 且导出宿主要求的 cliproxy_plugin_init。
func checkELF(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("不是有效 ELF：%w", err)
	}
	defer f.Close()
	if f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
		return fmt.Errorf("需要 x86-64 ELF，实际 %s/%s", f.Class, f.Machine)
	}
	syms, err := f.DynamicSymbols()
	if err != nil {
		return fmt.Errorf("读取动态符号失败：%w", err)
	}
	for _, s := range syms {
		if s.Name == "cliproxy_plugin_init" && s.Section != elf.SHN_UNDEF {
			return nil
		}
	}
	return fmt.Errorf("共享库未导出 cliproxy_plugin_init")
}

// buildSnippet 把示例配置缩进到 plugins.configs.privacygateway 之下。
func buildSnippet(example []byte) []byte {
	var b strings.Builder
	b.WriteString("# 把本片段合并进 CPA 的 config.yaml（合并，不要整份替换）。\n")
	b.WriteString("# 宿主要求同时启用 plugins.enabled 与 plugins.configs." + pluginID + ".enabled。\n")
	b.WriteString("plugins:\n  enabled: true\n  # dir: plugins   # 默认值，相对 CPA 工作目录\n  configs:\n    " + pluginID + ":\n")
	started := false
	for _, line := range strings.Split(strings.TrimRight(string(example), "\n"), "\n") {
		if !started {
			if strings.HasPrefix(line, "enabled:") || strings.HasPrefix(line, "# —— 生命周期") {
				started = true
			} else {
				continue
			}
		}
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("      " + line + "\n")
	}
	return []byte(b.String())
}

func checksums(entries []entry) []byte {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(e.data), e.name)
	}
	return []byte(b.String())
}

// buildZip 按名称排序写入条目，固定时间戳，输出可复现。
func buildZip(entries []entry) ([]byte, error) {
	sorted := append([]entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range sorted {
		if strings.Contains(e.name, "..") || strings.HasPrefix(e.name, "/") || strings.Contains(e.name, `\`) {
			return nil, fmt.Errorf("非法 zip 条目路径 %q", e.name)
		}
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: fixedTime}
		h.SetMode(e.mode)
		fw, err := w.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
