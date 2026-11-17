package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

func TestBuiltinManualContracts(t *testing.T) {
	doc, err := readBuiltin("references/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	yamlBlocks := regexp.MustCompile("(?s)```yaml\\n(.*?)```")
	for _, block := range yamlBlocks.FindAllSubmatch(doc, -1) {
		if _, err = config.ParseEcosystem(block[1]); err != nil {
			t.Fatal("发行手册配置无法解析", err)
		}
	}
	doc, err = readBuiltin("references/tools.md")
	if err != nil {
		t.Fatal(err)
	}
	jsonBlocks := regexp.MustCompile("(?s)```json\\n(.*?)\\n```")
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, block := range jsonBlocks.FindAllSubmatch(doc, -1) {
		var example struct {
			Tool      string
			Arguments json.RawMessage
		}
		if err = json.Unmarshal(block[1], &example); err != nil {
			t.Fatal(err)
		}
		if example.Tool == "" {
			continue
		}
		args, err := jsjson.Parse(string(example.Arguments))
		if err != nil {
			t.Fatal(err)
		}
		if err = ecosystem.ValidateArgs(PublicTools(), example.Tool, args); err != nil {
			t.Fatal("手册示例不符合当前契约", example.Tool, err)
		}
		if example.Tool == "read" {
			result, err := tools.Execute(t.Context(), dir, example.Tool, args)
			if err != nil || result.IsError || !strings.Contains(result.Text, "first") || !strings.Contains(result.Text, "second") {
				t.Fatalf("发行手册 read 示例语义错误 %+v %v", result, err)
			}
		}
	}
}
