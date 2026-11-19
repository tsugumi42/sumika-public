package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
)

// 登记表里标可写的每个键，都要有写入入口：把读出来的当前值写回去必须成功。
// 漏接的键会落到“设置没有写入入口”（或桌面、配置那几路的“不允许写这个键”），这里一次全列出来。
// 读出来不能原样写回的几项换成等价的合法值：空列表读出来是 null；QQ 账号的令牌只显示存没存，写入不收令牌。
// 带 * 的是通配说明行，具体到每个角色的行另有一条。
func TestEveryWritableSettingHasWriter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var r api.SettingsRegistry = &d.Reader
	ctx := context.Background()
	v, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	tried := 0
	for _, item := range v.Items {
		if item.ReadOnly || strings.Contains(item.Key, "*") {
			continue
		}
		raw, err := json.Marshal(item.Value)
		if err != nil {
			t.Fatal(item.Key, err)
		}
		switch {
		case item.Key == "reply.deny" && item.Value == nil:
			raw = json.RawMessage(`{}`)
		case item.Key == "linkReader.expand.forward.news" && item.Value == nil:
			raw = json.RawMessage(`[]`)
		case item.Key == "qq.accounts":
			var accounts []map[string]any
			if err := json.Unmarshal(raw, &accounts); err != nil {
				t.Fatal(err)
			}
			for _, acc := range accounts {
				delete(acc, "token")
			}
			raw, _ = json.Marshal(accounts)
		case item.Value == nil:
			bad = append(bad, item.Key+"：当前值为空，测试里补一个合法值")
			continue
		}
		next, err := r.WriteSetting(ctx, api.SettingChange{Key: item.Key, Value: raw}, v.Version)
		tried++
		if err != nil {
			bad = append(bad, item.Key+"："+err.Error())
			continue
		}
		v = next
	}
	if tried == 0 {
		t.Fatal("一个可写键都没试到")
	}
	if len(bad) > 0 {
		t.Fatalf("%d 个可写键写不进去：\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// 推特卡片配色、高度封顶经设置登记写入（设置工具走的这条），写完运行中的链接设置当场就变；非法值拒收、文件不动。
func TestTweetCardSettingsWritable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var r api.SettingsRegistry = &d.Reader
	ctx := context.Background()
	v, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []api.SettingChange{
		{Key: "linkReader.expand.tweetCard.theme", Value: json.RawMessage(`"light"`)},
		{Key: "linkReader.expand.tweetCard.maxHeight", Value: json.RawMessage(`1200`)},
	} {
		if v, err = r.WriteSetting(ctx, ch, v.Version); err != nil {
			t.Fatalf("%s：%v", ch.Key, err)
		}
	}
	if tc := a.currentLinks().Values.Expand.TweetCard; tc.Theme != "light" || tc.MaxHeight != 1200 {
		t.Fatalf("运行中的设置没变：%+v", tc)
	}
	file := filepath.Join(e.data, "config", "sumika.yml")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []api.SettingChange{
		{Key: "linkReader.expand.tweetCard.theme", Value: json.RawMessage(`"dim"`)},
		{Key: "linkReader.expand.tweetCard.theme", Value: json.RawMessage(`1`)},
		{Key: "linkReader.expand.tweetCard.maxHeight", Value: json.RawMessage(`100`)},
		{Key: "linkReader.expand.tweetCard.maxHeight", Value: json.RawMessage(`9000`)},
		{Key: "linkReader.expand.tweetCard.maxHeight", Value: json.RawMessage(`"960"`)},
		{Key: "linkReader.expand.tweetCard.maxHeight", Value: json.RawMessage(`960.5`)},
	} {
		if _, err := r.WriteSetting(ctx, ch, v.Version); err == nil {
			t.Fatalf("%s=%s 该拒收", ch.Key, ch.Value)
		}
		if after, _ := os.ReadFile(file); string(after) != string(before) {
			t.Fatalf("%s=%s 拒收后文件变了", ch.Key, ch.Value)
		}
	}
	if tc := a.currentLinks().Values.Expand.TweetCard; tc.Theme != "light" || tc.MaxHeight != 1200 {
		t.Fatalf("拒收不该动运行中的设置：%+v", tc)
	}
}

// 群开始表情登记的是“立即”生效：从设置登记写进去，运行中的链接设置当场就要变，且照链接那一路查编号。
// 以前它也在普通配置键表里，抢先走了普通写入，不刷新链接运行时，也不按表情编号报错。
func TestStartEmojiSettingAppliesNow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	var r api.SettingsRegistry = &d.Reader
	ctx := context.Background()
	v, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, err = r.WriteSetting(ctx, api.SettingChange{Key: "linkReader.expand.startEmoji", Value: json.RawMessage(`"77"`)}, v.Version); err != nil {
		t.Fatal(err)
	}
	if got := a.currentLinks().Values.Expand.StartEmoji; got != "77" {
		t.Fatalf("运行中的开始表情：%q", got)
	}
	if _, err := r.WriteSetting(ctx, api.SettingChange{Key: "linkReader.expand.startEmoji", Value: json.RawMessage(`"0"`)}, v.Version); err == nil || !strings.Contains(err.Error(), "表情编号") {
		t.Fatalf("编号 0 该按表情编号拒收：%v", err)
	}
}
