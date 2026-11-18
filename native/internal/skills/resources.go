package skills

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/goccy/go-yaml"
	"sumika-native/internal/config"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/mcpplugin"
	"sumika-native/internal/subagent"
	"sumika-native/internal/tools"
	"sumika-native/internal/web"
)

// 参考表从正在执行的声明和默认值生成，发行物无需源码目录。
func PublicTools() []tools.Declaration {
	out := append([]tools.Declaration(nil), tools.OwnerConsole()...)
	out = append(out, tools.MemoryDecls()...)
	out = append(out, Declaration)
	out = append(out, mcpplugin.Declarations...)
	out = append(out, web.Declarations...)
	return append(out, subagent.Declarations...)
}
func readBuiltin(resource string) ([]byte, error) {
	b, err := builtin.ReadFile("builtin/sumika/" + resource)
	if err != nil {
		return nil, err
	}
	text := string(b)
	if strings.Contains(text, "<!-- 生成：工具参考表 -->") {
		var table strings.Builder
		table.WriteString("工具参数 schema（是否可用仍取决于本轮授权）：\n\n")
		for _, d := range PublicTools() {
			fmt.Fprintf(&table, "### `%s`\n\n%s\n\n```json\n%s\n```\n\n", d.Name, d.Description, d.Parameters)
		}
		text = strings.Replace(text, "<!-- 生成：工具参考表 -->", table.String(), 1)
	}
	if strings.Contains(text, "<!-- 生成：配置参考表 -->") {
		raw, err := yaml.Marshal(config.EcosystemDefaults())
		if err != nil {
			return nil, err
		}
		text = strings.Replace(text, "<!-- 生成：配置参考表 -->", "生态配置的真实默认值（消费额度缺失表示禁止新出网）：\n\n```yaml\n"+string(raw)+"```", 1)
	}
	return []byte(text), nil
}
func ManualVersion() (string, error) {
	files := map[string]json.RawMessage{}
	err := fs.WalkDir(builtin, "builtin/sumika", func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := readBuiltin(strings.TrimPrefix(file, "builtin/sumika/"))
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(string(data))
		files[file] = raw
		return nil
	})
	return ecosystem.Digest(files), err
}
