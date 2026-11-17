package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"testing"
)

// 改思考不能顺带重试之前没开的整理；只有后台模型的没有→有转换才跑开整理前提。
func TestModelSettingsDoesNotRetrySummaryOnThinking(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	goMemoryDB(t, e, testChar)
	backup := filepath.Join(t.TempDir(), "fixture-backups") // 备份目录不能在数据目录里面（ResolveDir 拒绝）
	e.configure("backup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n")
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// remember 首次备份已做完；只让首次整理备份失败，再恢复目录。
	moved := backup + "-saved"
	if err := os.Rename(backup, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("阻止创建整理备份"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err = r.SetModelSettings(context.Background(), config.ModelChange{Field: config.BackgroundModel, String: new(testModel)}, v.Version)
	var partial *api.SavedModelSettingsError
	if !errors.As(err, &partial) || a.memories[testChar].SummarizeEnabled() {
		t.Fatalf("首次失败不正确 %v", err)
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, backup); err != nil {
		t.Fatal(err)
	}
	v, err = r.SetModelSettings(context.Background(), config.ModelChange{Field: config.ThinkingLocal, String: new("high")}, v.Version)
	if a.memories[testChar].SummarizeEnabled() {
		t.Fatal("改思考偷偷重试并打开了总结")
	}
	if !errors.As(err, &partial) || len(v.ApplyErrors) == 0 || v.Running.Local != "high" {
		t.Fatalf("前次未生效的原因丢了或思考没应用：%v %+v", err, v.ApplyErrors)
	}
}
