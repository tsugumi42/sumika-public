package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 主人 QQ 私聊审批的配置键（规格-主人QQ私聊审批）：默认关，错类型不能回成默认的成功。
func TestRemoteApprovalDefaultsOff(t *testing.T) {
	c := Defaults()
	if c.QQ.RemoteApproval.Enabled {
		t.Fatal("默认必须关")
	}
	if c.QQ.RemoteApproval.AwayAfterSeconds != 60 {
		t.Fatalf("默认等 60 秒，现在是 %d", c.QQ.RemoteApproval.AwayAfterSeconds)
	}
	parsed, err := Parse([]byte("qq:\n  owner: 200\n"), "编的.yml")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.QQ.RemoteApproval.Enabled {
		t.Fatal("配置里没写也必须是关")
	}
}

func TestRemoteApprovalConfigShapes(t *testing.T) {
	for _, tc := range []struct {
		name, yml string
		ok, on    bool
	}{
		{"明确开", "qq:\n  remoteApproval:\n    enabled: true\n", true, true},
		{"明确关", "qq:\n  remoteApproval:\n    enabled: false\n", true, false},
		{"字符串不算", "qq:\n  remoteApproval:\n    enabled: \"yes\"\n", false, false},
		{"数字不算", "qq:\n  remoteApproval:\n    enabled: 1\n", false, false},
		{"上限内", "qq:\n  remoteApproval:\n    awayAfterSeconds: 600\n", true, false},
		{"零是一出单就发", "qq:\n  remoteApproval:\n    awayAfterSeconds: 0\n", true, false},
		{"负数拒", "qq:\n  remoteApproval:\n    awayAfterSeconds: -1\n", false, false},
		{"超上限拒", "qq:\n  remoteApproval:\n    awayAfterSeconds: 601\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.yml), "编的.yml")
			if tc.ok && err != nil {
				t.Fatalf("应该能解析：%v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("错的形状不能当成默认的成功")
			}
			if tc.ok && c.QQ.RemoteApproval.Enabled != tc.on {
				t.Fatalf("开关是 %v", c.QQ.RemoteApproval.Enabled)
			}
		})
	}
}

// 走设置页同一条写回路：写进文件、重读得到同样的值，注释不丢；错类型报错、文件不变。
func TestRemoteApprovalSettingSaved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sumika.yml")
	raw := "# 注释留着\nqq:\n  owner: \"200\" # 编的\n"
	if err := os.WriteFile(file, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(raw), file)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	saved, err := SaveSetting(context.Background(), file, filepath.Join(dir, "history"), c.Version, SettingChange{Key: "qq.remoteApproval.enabled", Value: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.QQ.RemoteApproval.Enabled {
		t.Fatal("写回后应该是开")
	}
	saved, err = SaveSetting(context.Background(), file, filepath.Join(dir, "history"), saved.Version, SettingChange{Key: "qq.remoteApproval.awayAfterSeconds", Value: json.Number("0")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.QQ.RemoteApproval.AwayAfterSeconds != 0 {
		t.Fatalf("等待秒数是 %d", saved.QQ.RemoteApproval.AwayAfterSeconds)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# 注释留着") {
		t.Fatalf("注释丢了：%s", b)
	}
	if _, err = SaveSetting(context.Background(), file, filepath.Join(dir, "history"), saved.Version, SettingChange{Key: "qq.remoteApproval.enabled", Value: "true"}, now); err == nil {
		t.Fatal("字符串 true 不能写进开关")
	}
	if _, err = SaveSetting(context.Background(), file, filepath.Join(dir, "history"), saved.Version, SettingChange{Key: "qq.remoteApproval.awayAfterSeconds", Value: json.Number("601")}, now); err == nil {
		t.Fatal("超范围不能写")
	}
}
