package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/lineend"
)

// tools.lineEnding：配置里写了，Open 以后写文件就按它；设置页改了，当场生效、不用重启。
// 没有 t.Parallel：lineend 的默认值是进程里的一份，并行的测试每次 Open 都会把它重设成配置里的值。
func TestToolsLineEndingFollowsConfigAndSetting(t *testing.T) {
	e := newEnv(t)
	e.configure("tools:\n  lineEnding: lf\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close(); lineend.SetDefault("auto") })
	probe := filepath.Join(t.TempDir(), "n.txt") // 没有已有文件、.editorconfig、仓库：落到配置
	if got := lineend.Final(probe, nil, "甲\r\n乙\n"); got != "甲\n乙\n" {
		t.Fatalf("配置写 lf，Open 以后该写 LF：%q", got)
	}
	ctx := context.Background()
	v, err := d.Reader.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Reader.WriteSetting(ctx, api.SettingChange{Key: "tools.lineEnding", Value: json.RawMessage(`"bogus"`)}, v.Version); err == nil {
		t.Fatal("不认识的换行写进了配置")
	}
	if lineend.Default() != "lf" {
		t.Fatalf("写失败不该动运行中的值：%q", lineend.Default())
	}
	if v, err = d.Reader.ReadSettingsRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := d.Reader.WriteSetting(ctx, api.SettingChange{Key: "tools.lineEnding", Value: json.RawMessage(`"crlf"`)}, v.Version)
	if err != nil {
		t.Fatal(err)
	}
	if view.Effect != "立即" {
		t.Fatalf("生效说法 %q", view.Effect)
	}
	if got := lineend.Final(probe, nil, "甲\r\n乙\n"); got != "甲\r\n乙\r\n" {
		t.Fatalf("设置改成 crlf 以后该写 CRLF：%q", got)
	}
}
