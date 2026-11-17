package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/memory"
)

func TestModelSettingsSummaryActivation(t *testing.T) {
	t.Parallel()
	for _, catchup := range []string{"latest", ""} {
		t.Run("catchup="+catchup, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			goMemoryDB(t, e, testChar)
			backup := filepath.Join(t.TempDir(), "fixture-backups") // 备份目录不能在数据目录里面（ResolveDir 拒绝）
			e.configure("defaults:\n  backgroundModel: '   '\nbackup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: '" + catchup + "'\n")
			a := e.open()
			defer a.Close()
			d := a.Desktop()
			defer d.Close()
			r := &d.Reader
			m := a.memories[testChar]
			if !m.Writable() || m.SummarizeEnabled() {
				t.Fatal("初始权限不对")
			}
			v, err := r.ModelSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			v, err = r.SetModelSettings(context.Background(), config.ModelChange{Field: config.BackgroundModel, String: new(testModel)}, v.Version)
			if catchup == "" {
				var saved *api.SavedModelSettingsError
				if !errors.As(err, &saved) || v.Configured.BackgroundModel != testModel || len(v.ApplyErrors) == 0 || m.SummarizeEnabled() {
					t.Fatalf("部分失败未如实返回：%+v %v", v, err)
				}
			} else {
				if err != nil || !m.SummarizeEnabled() {
					t.Fatalf("未开整理 %v", err)
				}
				if _, ok, err := m.Meta(context.Background(), memory.MetaSummarizeEnabledAt); err != nil || !ok {
					t.Fatal("缺元数据")
				}
				entries, err := os.ReadDir(backup)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range entries {
					if strings.Contains(entry.Name(), "before-summarize") {
						found = true
					}
				}
				if !found {
					t.Fatal("没做总结前备份")
				}
			}
			modelSave(t, r, v, config.ModelChange{Field: config.BackgroundModel, String: new("")})
			if m.SummarizeEnabled() || !m.Writable() {
				t.Fatal("清空模型后整理或记忆写权限错误")
			}
		})
	}
}
func TestModelSettingsNoImplicitWriteAndSendGate(t *testing.T) {
	t.Parallel()
	e, a, peer := sendApp(t, "全部齐全")
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	startSend(t, e, a)
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	out := channel.OutletConfig{Check: a.checkQQSend}
	mode := a.qqSendMode(func(types.Conversation) string { return channel.ModeSpeak }, &out)
	if mode(conv) != channel.ModeSpeak {
		t.Fatal("前提齐全时未允许发送")
	}
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	modelSave(t, r, v, config.ModelChange{Field: config.BackgroundModel, String: new("")})
	if a.memories[testChar].Writable() {
		t.Fatal("设置后台模型顺带打开了记忆写入")
	}
	if a.guardFull(testChar) || mode(conv) != channel.ModeDryRun {
		t.Fatal("移除后台模型后发送门未收紧")
	}
	if peer.sends.Load() != 0 {
		t.Fatal("设置测试不应发送消息")
	}
}
