package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "my.json")
	if err := os.WriteFile(p, []byte(`{"page_size": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCfg(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.PageSize != 7 {
		t.Errorf("PageSize = %d，想要 7", c.PageSize)
	}
	if c.Currency == "" {
		t.Error("没写的项应该保留默认值")
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := LoadCfg(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("文件不存在应该报错")
	}
}

func TestBadJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	var c Cfg
	var err error
	if c, err = LoadCfg(p); err == nil {
		t.Errorf("坏 JSON 应该报错，拿到 %+v", c)
	}
}
