package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 根配置有效也不能盖过数据目录的 Stat 错误；Load 和 LoadIn 都要停下。
func TestConfigStatFailureStopsLoad(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("SUMIKA_CONFIG", "")
	t.Setenv("SUMIKA_DATA_DIR", data)
	if err := os.WriteFile(filepath.Join(root, "sumika.yml"), []byte("group: { frequency: 0.7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(data, "config")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sumika.yml")
	if err := os.Symlink("sumika.yml", file); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(file)
	if statErr == nil || errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("循环链接没有产生非不存在错误：%v", statErr)
	}
	for name, load := range map[string]func() (Config, error){
		"LoadIn": func() (Config, error) { return LoadIn(root, data) },
		"Load":   func() (Config, error) { return Load(root) },
	} {
		t.Run(name, func(t *testing.T) {
			c, err := load()
			if err == nil || c.Group.Frequency == 0.7 {
				t.Fatalf("Stat 错误后读了根配置：frequency=%v err=%v", c.Group.Frequency, err)
			}
		})
	}
}
