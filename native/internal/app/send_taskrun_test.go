package app

// 规格-运行页 第七节、盘点-静默失败 第一类第 1 条：QQ 发送被钳成只存不回时，从无到有、换原因记一行并提醒，
// 恢复时清掉提醒；每条消息不重复记。借发送门测试的装配（只有字面 = 守卫没到 full，群被钳）。

import (
	"strings"
	"testing"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/taskrun"
)

func TestSendClampRecordsOnTransitionOnly(t *testing.T) {
	t.Parallel()
	_, a, _ := sendApp(t, "只有字面")
	a.cfg.QQ.Reply = true
	var rows []string
	a.degrade.Sink = rowsSink{&rows}
	outCfg := channel.OutletConfig{Stores: a.set, Roster: a.roster, Adapters: []types.Adapter{a.adapter}, Check: a.checkQQSend, Logf: a.logf}
	mode := a.qqSendMode(func(types.Conversation) string { return channel.ModeSpeak }, &outCfg)
	for i := 0; i < 3; i++ {
		if m := mode(moreGroupConv); m != channel.ModeDryRun {
			t.Fatalf("守卫没 full 的群应被钳：%s", m)
		}
	}
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "send/clamped/degraded") {
		t.Fatalf("同一个原因连发三条只记一行：%v", rows)
	}
	var found bool
	for _, x := range a.alerts.Open() {
		if strings.Contains(x.Key, ":clamp:group") && strings.Contains(x.Title, "QQ 群") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有一条钳制提醒：%+v", a.alerts.Open())
	}
}

type rowsSink struct{ rows *[]string }

func (s rowsSink) AddTaskRun(r taskrun.Row) error {
	*s.rows = append(*s.rows, r.Module+"/"+r.Class+"/"+r.Outcome)
	return nil
}
