// Command lint 强制 cpa 的品味不变量（规则 §4.2 T1/T2/T6）。
// 每条违规都输出可执行的修复指令，便于智能体据此直接修正。
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxFileLines = 500
)

type violation struct {
	file string
	line int
	rule string
	msg  string
}

func main() {
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint: 无法定位模块根目录："+err.Error())
		os.Exit(2)
	}
	files, err := goFiles(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint: 扫描失败："+err.Error())
		os.Exit(2)
	}

	var violations []violation
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		violations = append(violations, checkFileSize(rel, path)...)
		violations = append(violations, checkStructuredLogging(rel, path)...)
	}
	violations = append(violations, checkDuplicateBodies(root, files)...)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	if len(violations) == 0 {
		fmt.Printf("lint: 通过（%d 个文件，阈值：单文件 ≤ %d 行）\n", len(files), maxFileLines)
		return
	}
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "%s:%d [%s] %s\n", v.file, v.line, v.rule, v.msg)
	}
	fmt.Fprintf(os.Stderr, "\nlint: 发现 %d 处违规。请按上述修复指令处理。\n", len(violations))
	os.Exit(1)
}

// moduleRoot 返回包含 go.mod 的目录。
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("向上未找到 go.mod")
		}
		dir = parent
	}
}

// goFiles 收集需要检查的 Go 源文件（排除 scripts/ 与 internal/ 工具目录）。
func goFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "build" || name == "scripts" || name == "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// T1：文件大小上限。
func checkFileSize(rel, path string) []violation {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for lines.Scan() {
		count++
	}
	if count <= maxFileLines {
		return nil
	}
	return []violation{{
		file: rel, line: 1, rule: "T1-file-size",
		msg: fmt.Sprintf("文件 %d 行，超过上限 %d 行。修复：按职责拆分为多个文件，每个文件单一职责；目标是把该文件降到 %d 行以内。", count, maxFileLines, maxFileLines),
	}}
}

// T2：结构化日志——禁止非结构化输出。
func checkStructuredLogging(rel, path string) []violation {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}
	banned := map[string]string{
		"Print":   "fmt.Print",
		"Printf":  "fmt.Printf",
		"Println": "fmt.Println",
	}
	var out []violation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if hint, bad := banned[sel.Sel.Name]; bad && pkg.Name == "fmt" {
			pos := fset.Position(call.Pos())
			out = append(out, violation{
				file: rel, line: pos.Line, rule: "T2-structured-log",
				msg: fmt.Sprintf("使用了 %s。修复：改为结构化日志，调用 logHost(ctx, level, message, map[string]any{...})，禁止向标准输出打印。", hint),
			})
		}
		if (sel.Sel.Name == "Println" || sel.Sel.Name == "Print" || sel.Sel.Name == "Printf") && pkg.Name == "log" {
			pos := fset.Position(call.Pos())
			out = append(out, violation{
				file: rel, line: pos.Line, rule: "T2-structured-log",
				msg: "使用了 log 包的非结构化输出。修复：改为 logHost(ctx, level, message, map[string]any{...})。",
			})
		}
		return true
	})
	return out
}

// T6：共享工具优先——检测跨文件重复的函数实现。
func checkDuplicateBodies(root string, files []string) []violation {
	type entry struct {
		rel  string
		line int
	}
	seen := map[string]entry{}
	var out []violation
	fset := token.NewFileSet()
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := fn.Name.Name + "|" + normalizeBody(fn.Body)
			if prev, ok := seen[key]; ok {
				out = append(out, violation{
					file: rel, line: fset.Position(fn.Pos()).Line, rule: "T6-shared-util",
					msg: fmt.Sprintf("函数 %s 与 %s:%d 的实现重复。修复：抽取为共享工具函数，两处复用同一实现。", fn.Name.Name, prev.rel, prev.line),
				})
				continue
			}
			seen[key] = entry{rel: rel, line: fset.Position(fn.Pos()).Line}
		}
	}
	return out
}

// normalizeBody 生成忽略空白与注释的函数体指纹。
func normalizeBody(body *ast.BlockStmt) string {
	var sb strings.Builder
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		switch n.(type) {
		case *ast.Ident, *ast.BasicLit, *ast.SelectorExpr, *ast.BinaryExpr, *ast.CallExpr, *ast.AssignStmt, *ast.ReturnStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.IndexExpr, *ast.StarExpr, *ast.UnaryExpr, *ast.CompositeLit, *ast.KeyValueExpr:
			sb.WriteString(fmt.Sprintf("%T", n))
		}
		return true
	})
	return sb.String()
}
