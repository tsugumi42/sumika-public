package archcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKernelHasNoWireFormat 钉住“内核只认中立对话”：
// internal/turn、internal/kernel、internal/app 的非测试 Go 文件里不许出现 OpenAI 聊天格式的字段名字面量，
// 也不许 import openaichat —— 线上格式只能待在 internal/llm/openaichat（规格-模型对话编解码 第 4 条）。
// 只查带双引号的写法：结构体 tag、普通字符串都算；注释里不带引号的名字不拦。
func TestKernelHasNoWireFormat(t *testing.T) {
	const openaichatPkg = "sumika-native/internal/llm/openaichat"
	packages := []string{"internal/turn", "internal/kernel", "internal/app"}
	forbidden := []string{
		`"tool_call_id"`,
		`"tool_calls"`,
		`"reasoning_content"`,
		`"reasoning_details"`,
		`"reasoning_text"`,
		`"stream_options"`,
		`"max_completion_tokens"`,
		`"prompt_cache_key"`,
	}

	root := moduleRoot(t)
	scanned := make(map[string]int, len(packages))
	for _, pkg := range packages {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读 native/%s：%v", pkg, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			scanned[pkg]++
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("读 native/%s/%s：%v", pkg, name, err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				for _, lit := range forbidden {
					if strings.Contains(line, lit) {
						t.Errorf("native/%s/%s:%d 出现线上字段名 %s：线上格式只能在 internal/llm/openaichat 里（规格-模型对话编解码 第 4 条）", pkg, name, i+1, lit)
					}
				}
			}
		}
		if scanned[pkg] == 0 {
			t.Fatalf("native/%s 下一个非测试 Go 文件都没扫到：目录写错或扫描器坏了，不能什么也没查就过", pkg)
		}
	}

	all := allPackages(t)
	if !all[openaichatPkg] {
		t.Fatalf("模块里没有 %s：改名或搬家了就把这条规则一起改", openaichatPkg)
	}
	info := moduleInfo(t)
	for _, pkg := range packages {
		importPath := importPathOf("./" + pkg)
		p, ok := info[importPath]
		if !ok {
			t.Fatalf("go list ./... 里没有 %s：目录改名了就把这条规则一起改", importPath)
		}
		for _, imp := range p.Imports {
			if imp == openaichatPkg {
				t.Errorf("%s 在非测试代码里 import 了 %s：内核只认 internal/llm/chat，线上格式只能在 internal/llm/openaichat 里（规格-模型对话编解码 第 4 条）", importPath, openaichatPkg)
			}
		}
	}
}
