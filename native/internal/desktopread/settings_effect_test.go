package desktopread

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/settingsreg"
)

func readDefaultSettings(t *testing.T) map[string]api.Result[api.Setting] {
	t.Helper()
	c := config.Defaults()
	c.Characters = map[string]config.CharacterModels{"anne": {}}
	reader := SettingsReader{Load: func(context.Context) (config.Config, error) { return c, nil }}
	result, err := reader.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]api.Result[api.Setting]{}
	for _, item := range result.Value.Items.Items {
		items[item.Value.Key] = item
	}
	return items
}

// 设置页投影的“已启用”和注册表的生效方式是同一句话：注册表说尚未生效的，投影不许说已启用；反过来也一样。
// 没有登记的键也不许进投影（它们没有生效方式可查）。
func TestProjectionAgreesWithRegistryEffect(t *testing.T) {
	// 这几个键在投影里是“程序当下的状态”（Running 给值才显示），和注册表里同名的“规划项”不是一回事，
	// 比如 platform.connection：这里是“连上协议端没有”，注册表里是“按平台启停接收”（尚未实现）。
	// 没有 Running 值时它们两边都是尚未接入，这里只核对这一点。
	runtimeStatus := map[string]bool{"occupation": true, "platform.selectedAccount": true, "platform.connection": true, "notifications.alerts": true, "notifications.autostart": true, "shortcuts.registered": true}
	items := readDefaultSettings(t)
	checked := 0
	for key, item := range items {
		entry, ok := settingsreg.Lookup(key)
		if item.Value.Source == "runtime" && !ok {
			continue // appearance.*、data.* 这些只来自程序当下的状态，不是配置键
		}
		if !ok {
			t.Errorf("%s 在设置页投影里，注册表没登记，没法说它生不生效", key)
			continue
		}
		checked++
		if runtimeStatus[key] {
			if item.State != api.Unsupported || item.Value.Supported {
				t.Errorf("%s 没有运行状态时应是尚未接入：%+v", key, item)
			}
			continue
		}
		if entry.Inert() {
			// 设置页画的是这里的中文名和一行说明，不是注册表的说明：没人读的项，这两处也不许说什么时候生效
			for _, text := range []string{item.Value.Name, item.Value.Help} {
				if strings.Contains(text, "重启") || strings.Contains(text, "生效") {
					t.Errorf("%s 尚未生效，设置页的文字却在说什么时候生效：%q", key, text)
				}
			}
		}
		if item.Value.Supported == entry.Inert() {
			t.Errorf("%s 投影说已启用=%v，注册表的生效方式是 %s", key, item.Value.Supported, entry.Effect)
		}
		if (item.State == api.Unsupported) != entry.Inert() {
			t.Errorf("%s 投影状态 %v 和注册表的生效方式 %s 对不上", key, item.State, entry.Effect)
		}
	}
	if checked < 60 {
		t.Fatalf("只核对了 %d 个键，投影被改小了？", checked)
	}
}

// 草稿里投影说“还没启用”、其实有代码在读的键：逐个核对过读取处，现在都是已启用；
// 反过来，投影说已启用、其实只有展示没有读取的键，现在是尚未启用。
func TestProjectionFixesStaleDisabledAndEnabledKeys(t *testing.T) {
	items := readDefaultSettings(t)
	live := []string{
		"vision.direct", "vision.directMax", // app/images.go:130,134
		"tools.workdir", "tools.approvalMinutes", "tools.approvalMode", "tools.autoApproveDirs", // app/app.go:476-489、974；host.go:454
		"backup.dir", "backup.daily", "backup.dailyMedia", "backup.alertGB", "backup.minFreeGB", "backup.runMaxHours", "backup.closeWaitSeconds", // app/backup.go:68-77
	}
	for _, key := range live {
		it, ok := items[key]
		if !ok || !it.Value.Supported || it.State != api.Ready {
			t.Errorf("%s 有代码在读，应显示已启用：%+v", key, it)
		}
	}
	inert := []string{
		"vision.model", "vision.enabled", "vision.concurrency", "vision.backfillLanes", // vision.model 只在模型页作展示（app/models.go:266）
		"memory.idleMinutes", "memory.batchEvents", "memory.topK", "memory.maxChars",
		"expression.learnGroups", "expression.learnPrivate", "expression.learnHome", "expression.autoApprove", "expression.reviewHours", "expression.reviewBatches",
		"agent.context.topicCompactAt", "characters.anne.topic", // 话题还没做：以前投影说已启用
	}
	for _, key := range inert {
		it, ok := items[key]
		if !ok || it.Value.Supported || it.State != api.Unsupported {
			t.Errorf("%s 后台没人读，应显示还没启用：%+v", key, it)
		}
	}
}
