package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/envcheck"
)

// 宿主那份快照拷成 doctor 的协议结构：时间、模式、各项和子项都原样过去。
func TestEnvDoctorSnapshotCopiesItemsAndSubs(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	s := envcheck.Snapshot{At: at, Mode: envcheck.ModeReceive, Items: []envcheck.Item{{
		ID: "env.tools", Group: "外部程序", Name: "外部程序", State: envcheck.Warn,
		Detail: "登记了 1 个外部程序，1 个没找到", Next: "装 PowerShell 7", Affects: "外部程序：个别可选功能用不了",
		Sub: []envcheck.SubItem{{Name: "pwsh", State: envcheck.Warn, Detail: "没找到", Next: "装 PowerShell 7"}},
	}}}
	d := envDoctorSnapshot(s)
	if !d.At.Equal(at) || d.Mode != "receive" || len(d.Items) != 1 {
		t.Fatalf("快照头没拷过去：%+v", d)
	}
	it := d.Items[0]
	if it.ID != "env.tools" || it.State != "warn" || it.Next != "装 PowerShell 7" || len(it.Sub) != 1 {
		t.Fatalf("项没拷过去：%+v", it)
	}
	if sub := it.Sub[0]; sub.Name != "pwsh" || sub.State != "warn" || sub.Next != "装 PowerShell 7" {
		t.Fatalf("子项没拷过去：%+v", sub)
	}
	// 没有子项的项：协议里不带 sub，不写 null
	b, err := json.Marshal(envDoctorSnapshot(envcheck.Build(envcheck.Inputs{})))
	if err != nil || strings.Contains(string(b), `"sub":null`) {
		t.Fatalf("空子项不该写成 null：%s %v", b, err)
	}
}

// status 的那句：有问题、要注意、不知道都照实说，全正常才写“正常”。
func TestEnvStatusLine(t *testing.T) {
	item := func(state envcheck.State) envcheck.Item { return envcheck.Item{State: state} }
	cases := []struct {
		name  string
		items []envcheck.Item
		want  string
	}{
		{"全正常", []envcheck.Item{item(envcheck.OK), item(envcheck.NA)}, "正常"},
		{"还没有快照", nil, ""},
		{"有问题", []envcheck.Item{item(envcheck.Bad), item(envcheck.OK)}, "1 项有问题（sumika-ctl doctor 看详情）"},
		{"有问题也有注意", []envcheck.Item{item(envcheck.Bad), item(envcheck.Warn), item(envcheck.Warn)}, "1 项有问题、2 项要注意（sumika-ctl doctor 看详情）"},
		{"不知道不能当正常", []envcheck.Item{item(envcheck.Unknown)}, "1 项不知道（sumika-ctl doctor 看详情）"},
	}
	for _, c := range cases {
		if got := envStatusLine(envcheck.Snapshot{Items: c.items}); got != c.want {
			t.Errorf("%s：%q，应是 %q", c.name, got, c.want)
		}
	}
}
