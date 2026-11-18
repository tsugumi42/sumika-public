package app

import (
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/store"
)

// 经过现有桌面缺口接口确认、重读、手动补录和撤销；确认不等于 filled 或完整覆盖。
func TestGapAcknowledgementLifecycle(t *testing.T) {
	t.Parallel()
	for _, c := range []types.Conversation{backfillPrivate(), backfillGroup()} {
		t.Run(c.Scene, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			ctx := manualConnection(t, e)
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "blocked")
			a := &App{set: e.set, backfill: e.b, account: testAccount, now: e.clk.Now, opt: Options{Mode: Receive}}
			e.b.groups = []string{testGroup}
			a.cfg.QQ.Backfill.Groups = true
			a.cfg.QQ.Groups = []config.QQID{config.QQID(testGroup)}
			reader := desktopread.New(e.set, []api.CharacterView{{ID: testChar}}, nil).WithBackfills(a.backfillProvider())
			defer reader.Close()
			actions, ok := any(reader).(api.GapAcknowledgements)
			if !ok {
				t.Fatal("现有缺口接口缺少可撤销确认动作")
			}
			before := e.through()
			if err := e.st.SettleQQBackfill(ctx, testAccount, before, []store.QQBackfillResult{{Conversation: g.Conversation, FromMS: g.FromMS}}); err != nil {
				t.Fatal(err)
			}
			proof, err := e.st.QQContinuousRange(ctx, testAccount, g.Conversation)
			if err != nil {
				t.Fatal(err)
			}
			ref := gapRef(g)
			confirmed, err := actions.SetGapAcknowledged(ctx, ref, true)
			if err != nil || confirmed.AcknowledgedMS == 0 || confirmed.State != "blocked" {
				t.Fatal("确认未保留受阻事实", confirmed, err)
			}
			e.clk.Advance(time.Second)
			twice, err := actions.SetGapAcknowledged(ctx, ref, true)
			if err != nil || twice.AcknowledgedMS != confirmed.AcknowledgedMS {
				t.Fatal("重复确认改变记录", twice, err)
			}
			page, err := reader.ReadAlignmentGaps(ctx, "", 100)
			if err != nil || len(page.Value.Items) != 1 || page.Value.Items[0].AcknowledgedMS != confirmed.AcknowledgedMS {
				t.Fatal("确认后缺口不可读", page, err)
			}
			j, err := reader.RequestGapBackfill(ctx, ref)
			if err != nil {
				t.Fatal("确认挡住手动补录", err)
			}
			drainManual(t, e, ctx, j)
			restored, err := actions.SetGapAcknowledged(ctx, ref, false)
			if err != nil || restored.AcknowledgedMS != 0 || restored.State != "blocked" {
				t.Fatal("撤销误清缺口", restored, err)
			}
			after, err := e.st.QQContinuousRange(ctx, testAccount, g.Conversation)
			if err != nil || after != proof || e.through() != before {
				t.Fatal("人工确认推进了位置", proof, after, err)
			}
			stale := ref
			stale.ToMS++
			_, err = actions.SetGapAcknowledged(ctx, stale, true)
			requireBackfillCode(t, err, "stale-gap")
			other := ref
			other.ConversationKey.Conversation = "qq:private:999:200"
			_, err = actions.SetGapAcknowledged(ctx, other, true)
			if err == nil {
				t.Fatal("跨账号确认被接受")
			}
			a.opt.Mode = Archive
			_, err = actions.SetGapAcknowledged(ctx, ref, true)
			requireBackfillCode(t, err, "readonly")
		})
	}
}
