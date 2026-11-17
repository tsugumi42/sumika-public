package main

import (
	"slices"
	"strconv"
	"testing"
)

// 设置页的分类清单（settings.go 的 settingsTabs）是按下标取的：左栏、内容区、图标、各页的键归属都认同一组 tabXxx。
// 这几个测试守的是“页名对应的内容是对的”：撤页、加页以后下标一旦错位，点哪页都不报错，只是内容对不上页名。

// categoryKey 是设置左栏里第 tab 个分类按钮的元素名（winshell 用 "category-"+分类编号，编号就是下标）。
func categoryKey(tab int) string { return "category-" + strconv.Itoa(tab) }

// 页名、图标、下标是同一张表；撤掉的两页（职业、快捷键）不再有。
func TestSettingsTabsStayPaired(t *testing.T) {
	want := map[int]string{
		tabLook: "外观", tabModels: "模型", tabMemory: "记忆和学习", tabTools: "工具与审批",
		tabAccounts: "平台账号", tabNotify: "通知", tabData: "数据", tabPlugins: "插件",
	}
	if len(settingsTabs) != len(want) {
		t.Fatalf("settingsTabs 有 %d 页，这里登记了 %d 页：%v", len(settingsTabs), len(want), settingsTabs)
	}
	for tab, name := range want {
		if settingsTabs[tab] != name {
			t.Errorf("第 %d 页应是 %q，实际 %q", tab, name, settingsTabs[tab])
		}
	}
	if len(wbSettingIcons) != len(settingsTabs) {
		t.Fatalf("图标 %d 个、页 %d 个：两张表要一一对应", len(wbSettingIcons), len(settingsTabs))
	}
	// 每页配哪个图标写死在这里：两个图标互换时按“非空”查不出来，按期望值才红。
	wantIcons := map[int]string{
		tabLook: "palette", tabModels: "ai", tabMemory: "brain", tabTools: "tool",
		tabAccounts: "qq", tabNotify: "bell", tabData: "folder", tabPlugins: "layers",
	}
	for tab, icon := range wantIcons {
		if wbSettingIcons[tab] != icon {
			t.Errorf("第 %d 页（%s）的图标应是 %q，实际 %q", tab, settingsTabs[tab], icon, wbSettingIcons[tab])
		}
	}
	for _, gone := range []string{"职业", "快捷键"} {
		if slices.Contains(settingsTabs, gone) {
			t.Errorf("%q 页是空页，已撤掉（待办 P0-6），不该回来；要做先有内容", gone)
		}
	}
}

// 每个键归哪一页：撤掉的两页的键不归任何一页，也不掉进“认不出前缀就落在数据页”的兜底。
func TestSettingPageKeepsRemovedKeysOffEveryPage(t *testing.T) {
	for _, key := range []string{"occupation", "shortcuts.registered", "shortcuts.anything"} {
		if got := settingPage(key); got != noSettingsPage {
			t.Errorf("%s 应不属于任何一页，实际落在第 %d 页（%s）", key, got, settingsTabs[got])
		}
	}
	for key, want := range map[string]int{
		"appearance.mode": tabLook, "models.chat": tabModels, "memory.topK": tabMemory, "tools.workdir": tabTools,
		"qq.owner": tabAccounts, "notifications.sound": tabNotify, "data.root": tabData, "backup.dir": tabData,
		"某个认不出的前缀.x": tabData,
	} {
		if got := settingPage(key); got != want {
			t.Errorf("%s 应在第 %d 页（%s），实际 %d", key, want, settingsTabs[want], got)
		}
	}
}

// 新外壳里点左栏每一页：选中的下标就是页名的下标，内容是这一页自己的行，别的页的行不出现，撤掉的两页的行哪页都没有。
func TestShellSettingsEveryCategoryOpensItsOwnPage(t *testing.T) {
	r := realSettingsOf(t, nil)
	nameCount := map[string]int{}
	for _, it := range r.Value.Items.Items {
		nameCount[it.Value.Name]++
	}
	marker := map[int]string{} // 每个靠注册表行画出来的页挑一行名字全局唯一的当记号
	var gone []string          // 不属于任何一页的行
	for _, it := range r.Value.Items.Items {
		p := settingPage(it.Value.Key)
		if p == noSettingsPage {
			gone = append(gone, it.Value.Name)
			continue
		}
		if p == tabLook || p == tabModels || p == tabPlugins || nameCount[it.Value.Name] != 1 || slices.Contains(settingsTabs, it.Value.Name) {
			continue
		}
		if _, ok := marker[p]; !ok {
			marker[p] = it.Value.Name
		}
	}
	rowTabs := []int{tabMemory, tabTools, tabAccounts, tabNotify, tabData}
	for _, tab := range rowTabs {
		if marker[tab] == "" {
			t.Fatalf("%s 页在注册表里一行也没有：没有空页（挑记号失败）", settingsTabs[tab])
		}
	}
	if len(gone) == 0 {
		t.Fatal("注册表里应还有不属于任何页的行（职业、快捷键的占位），这个测试没查到东西")
	}

	a := shellApp()
	a.rail = 4
	a.real = r
	tt := shellTester(a, 2048, 1030)
	for i, name := range settingsTabs {
		clickKey(t, tt, categoryKey(i))
		if a.realTab != i {
			t.Fatalf("点左栏“%s”后 realTab=%d，应为 %d", name, a.realTab, i)
		}
		for _, tab := range rowTabs {
			has := tt.HasText(marker[tab])
			if tab == i && !has {
				t.Errorf("“%s”页里没有它自己的行 %q：%v", name, marker[tab], tt.Texts())
			}
			if tab != i && has {
				t.Errorf("“%s”页里出现了“%s”页的行 %q", name, settingsTabs[tab], marker[tab])
			}
		}
		for _, g := range gone {
			if tt.HasText(g) {
				t.Errorf("“%s”页里出现了没有页面的行 %q", name, g)
			}
		}
	}
	for _, label := range []string{"职业", "快捷键"} {
		if tt.HasText(label) {
			t.Errorf("左栏不该再有“%s”", label)
		}
	}
}

// 演示线框（settingsView，只有 -demo 进得去）按下标分派页面：两个 case 对调时点哪页都不报错，只是内容对不上页名。
// 逐页打开，页里要有这一页特有的一行字，别页特有的字不许出现。
func TestDemoSettingsEveryTabShowsItsOwnPage(t *testing.T) {
	marker := map[int]string{
		tabLook: "配色预设", tabModels: "Magpie 地址", tabMemory: "记性", tabTools: "审批方式",
		tabAccounts: "收发自检", tabNotify: "什么事走哪条路", tabData: "放在哪、占了多少", tabPlugins: "内置插件",
	}
	if len(marker) != len(settingsTabs) {
		t.Fatalf("记号 %d 个、页 %d 个：加页时这里也要补一行", len(marker), len(settingsTabs))
	}
	for tab := range settingsTabs {
		a := &app{demoMode: true}
		a.set = newSettings(nil)
		a.set.tab = tab
		tt := testerOf(1300, 3000, a.settingsView)
		for other, m := range marker {
			has := tt.HasText(m)
			if other == tab && !has {
				t.Errorf("演示设置第 %d 页（%s）里没有它自己的字 %q：%v", tab, settingsTabs[tab], m, tt.Texts())
			}
			if other != tab && has {
				t.Errorf("演示设置第 %d 页（%s）里出现了“%s”页的字 %q", tab, settingsTabs[tab], settingsTabs[other], m)
			}
		}
	}
}
