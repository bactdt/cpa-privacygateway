// Package archtest 用测试方式强制 cpa 的分层依赖方向（规则 §4.1、§4.2 T7）。
// 结构测试校验的是"允许的依赖边"，不是运行时行为。
package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 分层白名单：key 为包目录（相对模块根），value 为它允许导入的"内部包"集合。
// 规则：依赖只能从外层指向内层。
//
//	main（插件入口层）→ redact（纯领域层）
//	redact 是纯领域层，禁止依赖 main，也禁止依赖宿主 SDK。
var allowedInternalImports = map[string]map[string]bool{
	".":      {"duanjuapp/cpa/redact": true},
	"redact": {},
}

// 领域层禁止导入的宿主 SDK 前缀（边界处解析数据形状，领域层不得耦合宿主）。
var forbiddenForDomain = []string{
	"github.com/router-for-me/CLIProxyAPI",
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("无法获取工作目录：%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("向上未找到 go.mod")
		}
		dir = parent
	}
}

// packageDirs 返回模块内除 scripts/internal 外的所有含 .go 文件的目录（相对模块根）。
func packageDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		name := info.Name()
		if path != root && (name == ".git" || name == "build" || name == "scripts" || name == "internal" || name == "docs") {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				rel, _ := filepath.Rel(root, path)
				dirs = append(dirs, rel)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描包目录失败：%v", err)
	}
	return dirs
}

func importPath(root, dir string) string {
	if dir == "." {
		return "duanjuapp/cpa"
	}
	return "duanjuapp/cpa/" + filepath.ToSlash(dir)
}

func TestDependencyDirection(t *testing.T) {
	root := moduleRoot(t)
	dirs := packageDirs(t, root)
	if len(dirs) == 0 {
		t.Fatal("未发现任何 Go 包目录")
	}
	for _, dir := range dirs {
		abs := filepath.Join(root, dir)
		pkg, err := build.ImportDir(abs, 0)
		if err != nil {
			t.Fatalf("解析包 %s 失败：%v", dir, err)
		}
		imports := append([]string{}, pkg.Imports...)
		allowed, known := allowedInternalImports[dir]
		for _, imp := range imports {
			if !strings.HasPrefix(imp, "duanjuapp/cpa") {
				continue
			}
			if !known {
				t.Errorf("包 %s 未在分层白名单中登记。修复：在 internal/archtest/arch_test.go 的 allowedInternalImports 中显式声明 %s 的允许依赖。", dir, dir)
				continue
			}
			if !allowed[imp] {
				t.Errorf("依赖方向违规：%s 导入了 %s，但白名单不允许。修复：删除该导入，或改为依赖允许的内层包；确需新增依赖边时，先在 ARCHITECTURE.md 更新分层规则并在白名单登记。", dir, imp)
			}
		}
		_ = importPath(root, dir)
	}
}

func TestDomainLayerHasNoHostSDK(t *testing.T) {
	root := moduleRoot(t)
	abs := filepath.Join(root, "redact")
	pkg, err := build.ImportDir(abs, 0)
	if err != nil {
		t.Fatalf("解析 redact 包失败：%v", err)
	}
	for _, imp := range pkg.Imports {
		for _, forbidden := range forbiddenForDomain {
			if strings.HasPrefix(imp, forbidden) {
				t.Errorf("领域层违规：redact 导入了宿主 SDK %s。修复：领域层必须保持纯净，把宿主相关逻辑上移到 main 包，通过参数传入。", imp)
			}
		}
	}
}

func TestNoImportCycles(t *testing.T) {
	root := moduleRoot(t)
	dirs := packageDirs(t, root)
	graph := map[string][]string{}
	for _, dir := range dirs {
		abs := filepath.Join(root, dir)
		pkg, err := build.ImportDir(abs, 0)
		if err != nil {
			t.Fatalf("解析包 %s 失败：%v", dir, err)
		}
		self := importPath(root, dir)
		for _, imp := range pkg.Imports {
			if strings.HasPrefix(imp, "duanjuapp/cpa") {
				graph[self] = append(graph[self], imp)
			}
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(node string, stack []string) bool
	visit = func(node string, stack []string) bool {
		color[node] = gray
		for _, next := range graph[node] {
			switch color[next] {
			case gray:
				t.Errorf("检测到导入环：%s → %s。修复：打破循环依赖，把共享逻辑下移到更低层包。", strings.Join(append(stack, node), " → "), next)
				return true
			case white:
				if visit(next, append(stack, node)) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}
	for _, dir := range dirs {
		self := importPath(root, dir)
		if color[self] == white {
			if visit(self, nil) {
				return
			}
		}
	}
}
