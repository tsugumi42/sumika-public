package app

import (
	"context"
	"encoding/json"
	"testing"

	"sumika-native/internal/api"
)

// 重试次数走完整装配：设置登记写 linkReader.expand.translate.retries → 配置 → 翻译器实际拿到的 Retries。
// 模型第一次一直不回（等到单次时限），第二次回译文：重试 1 次时发出译文、调两次；改成 0 次就只调一次、只发原文。
// 登记里漏了这个键、或者装配时没把 Retries 传给翻译器，这条都会红。
func TestLinkTranslateRetriesWiredFromSettings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		retries string
		calls   int32
		want    bool
	}{
		{"重试一次", "1", 2, true},
		{"不重试", "0", 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeModel{}
			m.reply = func(ctx context.Context) (string, error) {
				if m.calls.Load() == 1 {
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "阿尔忒弥斯二号完成了绕月飞行，乘组正在返程。", nil
			}
			e, a, peer := newTranslateHost(t, enText, m)
			d := a.Desktop()
			defer d.Close()
			var r api.SettingsRegistry = &d.Reader
			ctx := context.Background()
			v, err := r.ReadSettingsRegistry(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, ch := range []api.SettingChange{
				{Key: "linkReader.expand.translate.timeoutSeconds", Value: json.RawMessage("1")},
				{Key: "linkReader.expand.translate.retries", Value: json.RawMessage(c.retries)},
			} {
				if v, err = r.WriteSetting(ctx, ch, v.Version); err != nil {
					t.Fatalf("%s: %v", ch.Key, err)
				}
			}
			if got := a.currentLinks().Values.Expand.Translate.Retries; string(rune('0'+got)) != c.retries {
				t.Fatalf("配置里的重试次数：%d", got)
			}
			sendLink(t, e, a, "9131"+c.retries, "https://x.com/paper/status/7713"+c.retries)
			waitFor(t, "展开发出", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/7713"+c.retries) })
			if c.want {
				waitFor(t, "译文发出", func() bool { return linkSawFrames(peer, "绕月飞行") })
			} else {
				// 运行页只记没出译文的：最后一次也超时，记成 timeout
				waitFor(t, "运行页记一行超时", func() bool { return taskRunCount(t, a, "class='translate' AND outcome='timeout'") == 1 })
				if linkSawFrames(peer, "绕月飞行") {
					t.Fatal("不重试时不该有译文")
				}
			}
			if got := m.calls.Load(); got != c.calls {
				t.Fatalf("调了 %d 次模型，要 %d 次", got, c.calls)
			}
		})
	}
}
