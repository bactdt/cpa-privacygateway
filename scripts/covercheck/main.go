// Command covercheck 校验 Go 覆盖率下限（规则 §4.2 T10）。
// 整体下限与关键模块下限分开检查，低于下限即失败并给出修复指令。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

type threshold struct {
	prefix string
	name   string
	min    float64
}

type tally struct {
	total   int
	covered int
}

func main() {
	profile := flag.String("profile", "coverage.out", "Go 覆盖率 profile 路径")
	flag.Parse()

	thresholds := []threshold{
		{prefix: "", name: "整体", min: 70},
		{prefix: "duanjuapp/cpa/redact", name: "redact 包", min: 80},
	}

	perFile := map[string]*tally{}
	file, err := os.Open(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "covercheck: 无法打开覆盖率文件 "+*profile+"："+err.Error())
		fmt.Fprintln(os.Stderr, "修复：先执行 `go test -coverprofile=coverage.out ./...` 生成覆盖率报告。")
		os.Exit(2)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		path := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			continue
		}
		var statements, count int
		if _, err := fmt.Sscanf(fields[1], "%d", &statements); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(fields[2], "%d", &count); err != nil {
			continue
		}
		t := perFile[path]
		if t == nil {
			t = &tally{}
			perFile[path] = t
		}
		t.total += statements
		if count > 0 {
			t.covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "covercheck: 读取覆盖率文件失败："+err.Error())
		os.Exit(2)
	}

	failed := false
	for _, th := range thresholds {
		agg := &tally{}
		for path, t := range perFile {
			if th.prefix == "" || strings.HasPrefix(path, th.prefix) {
				agg.total += t.total
				agg.covered += t.covered
			}
		}
		if agg.total == 0 {
			fmt.Fprintf(os.Stderr, "covercheck: %s 无覆盖数据（前缀 %q）。修复：确认测试覆盖该包。\n", th.name, th.prefix)
			failed = true
			continue
		}
		pct := float64(agg.covered) / float64(agg.total) * 100
		status := "OK"
		if pct < th.min {
			status = "FAIL"
			failed = true
		}
		fmt.Printf("covercheck: %-10s %6.2f%% (下限 %.0f%%) %s\n", th.name, pct, th.min, status)
		if pct < th.min {
			fmt.Fprintf(os.Stderr, "covercheck: %s 覆盖率 %.2f%% 低于下限 %.0f%%。修复：为未覆盖分支补充单元测试，重点覆盖边界与异常路径。\n", th.name, pct, th.min)
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("covercheck: 覆盖率达标")
}
