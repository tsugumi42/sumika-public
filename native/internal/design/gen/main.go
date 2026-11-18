// gen 从 internal/design/tokens 生成 internal/design/tokens_gen.go（规格-设计令牌 3.4）。
// 在 internal/design 目录里跑：go generate（等于 go run ./gen）；加 -fix 先把算出来的令牌的 $value 改成算出来的值。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"sumika-native/internal/design/tokenspec"
)

func main() {
	fix := flag.Bool("fix", false, "把算出来的令牌的 $value 改写成算出来的值")
	dir := flag.String("dir", ".", "internal/design 目录")
	flag.Parse()
	if err := run(*dir, *fix); err != nil {
		fmt.Fprintln(os.Stderr, "生成设计令牌失败：", err)
		os.Exit(1)
	}
}

func run(dir string, fix bool) error {
	tokDir := filepath.Join(dir, "tokens")
	spec, err := tokenspec.Load(tokDir)
	if err != nil {
		return err
	}
	if fix {
		changed, err := spec.Fix()
		if err != nil {
			return err
		}
		for _, f := range changed {
			fmt.Println("改写了", f)
		}
		if spec, err = tokenspec.Load(tokDir); err != nil {
			return err
		}
	}
	m, err := spec.Build()
	if err != nil {
		return err
	}
	src, err := m.Go()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tokens_gen.go"), src, 0o644)
}
