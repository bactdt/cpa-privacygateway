// Command check-docs 校验文档知识库（规则 §2.4、§3 阶段 3 子任务 9）。
// 检查项：
//  1. 死链：所有 Markdown 相对链接必须可解析。
//  2. 索引完整性：docs/ 下每个 .md 必须被 docs/index.md（或其子索引）引用。
//  3. 必填节：AGENTS.md / ARCHITECTURE.md / QUALITY_SCORE.md 必须含约定小节。
//  4. 新鲜度：文档头部若含 "校验状态" 或 "已校验 YYYY-MM-DD"，日期不得早于 N 天前。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
var dateRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`)

type violation struct {
	file string
	line int
	msg  string
}

func main() {
	root := flag.String("root", ".", "仓库根目录")
	maxAge := flag.Int("max-age-days", 180, "文档新鲜度上限（天）")
	flag.Parse()

	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-docs: 无法解析根目录："+err.Error())
		os.Exit(2)
	}

	var violations []violation

	// 收集全部 .md 文件
	var mdFiles []string
	err = filepath.Walk(rootAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if path != rootAbs && (name == ".git" || name == "build" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".md") {
			rel, _ := filepath.Rel(rootAbs, path)
			mdFiles = append(mdFiles, rel)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-docs: 扫描失败："+err.Error())
		os.Exit(2)
	}
	sort.Strings(mdFiles)

	// 1. 死链检查
	for _, rel := range mdFiles {
		abs := filepath.Join(rootAbs, rel)
		f, err := os.Open(abs)
		if err != nil {
			violations = append(violations, violation{rel, 0, "无法读取：" + err.Error()})
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			for _, m := range linkRe.FindAllStringSubmatch(sc.Text(), -1) {
				target := strings.TrimSpace(m[1])
				if target == "" || strings.HasPrefix(target, "#") ||
					strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				// 去掉锚点
				if i := strings.Index(target, "#"); i >= 0 {
					target = target[:i]
				}
				if target == "" {
					continue
				}
				resolved := filepath.Join(filepath.Dir(abs), target)
				if _, err := os.Stat(resolved); err != nil {
					violations = append(violations, violation{rel, lineNo,
						fmt.Sprintf("死链：%q 无法解析。修复：改正路径，或先创建目标文件。", target)})
				}
			}
		}
		f.Close()
	}

	// 2. 索引完整性：docs/ 下每个 .md 应被某个 index.md 引用
	indexContent := map[string]string{}
	for _, rel := range mdFiles {
		if strings.HasSuffix(rel, "index.md") {
			b, err := os.ReadFile(filepath.Join(rootAbs, rel))
			if err == nil {
				indexContent[rel] = string(b)
			}
		}
	}
	allIndex := strings.Join(values(indexContent), "\n")
	for _, rel := range mdFiles {
		if !strings.HasPrefix(rel, "docs/") || strings.HasSuffix(rel, "index.md") {
			continue
		}
		base := filepath.Base(rel)
		if !strings.Contains(allIndex, base) {
			violations = append(violations, violation{rel, 0,
				fmt.Sprintf("索引缺失：%s 未被任何 index.md 引用。修复：在 docs/index.md（或子索引）中新增条目。", rel)})
		}
	}

	// 3. 必填节
	required := map[string][]string{
		"AGENTS.md":             {"项目", "命令", "约束"},
		"ARCHITECTURE.md":       {"分层", "依赖方向", "数据流"},
		"docs/QUALITY_SCORE.md": {"业务域", "差距"},
	}
	for rel, sections := range required {
		b, err := os.ReadFile(filepath.Join(rootAbs, rel))
		if err != nil {
			violations = append(violations, violation{rel, 0, "缺少必填文档。"})
			continue
		}
		text := string(b)
		for _, sec := range sections {
			if !strings.Contains(text, sec) {
				violations = append(violations, violation{rel, 0,
					fmt.Sprintf("必填节缺失：未找到关键词 %q。修复：补全该小节，或更新 check-docs 的必填节定义。", sec)})
			}
		}
	}

	// 4. 新鲜度
	cutoff := time.Now().AddDate(0, 0, -*maxAge)
	for _, rel := range mdFiles {
		if !strings.HasPrefix(rel, "docs/") && rel != "AGENTS.md" && rel != "ARCHITECTURE.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(rootAbs, rel))
		if err != nil {
			continue
		}
		text := string(b)
		if !strings.Contains(text, "校验状态") && !strings.Contains(text, "已校验") {
			continue
		}
		// 找到最晚日期
		var latest time.Time
		for _, m := range dateRe.FindAllStringSubmatch(text, -1) {
			t, err := time.Parse("2006-01-02", m[0])
			if err != nil {
				continue
			}
			if t.After(latest) {
				latest = t
			}
		}
		if !latest.IsZero() && latest.Before(cutoff) {
			violations = append(violations, violation{rel, 0,
				fmt.Sprintf("文档过期：最后校验日期 %s 早于 %d 天前。修复：复核内容后更新校验日期。",
					latest.Format("2006-01-02"), *maxAge)})
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			if v.line > 0 {
				fmt.Fprintf(os.Stderr, "%s:%d %s\n", v.file, v.line, v.msg)
			} else {
				fmt.Fprintf(os.Stderr, "%s %s\n", v.file, v.msg)
			}
		}
		fmt.Fprintf(os.Stderr, "\ncheck-docs: 发现 %d 处文档问题。\n", len(violations))
		os.Exit(1)
	}
	fmt.Printf("check-docs: 通过（%d 份文档，无死链，索引完整，必填节齐全）\n", len(mdFiles))
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
