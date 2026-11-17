package config

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// 群里谁发的链接能触发展开（用户 2026-11-13）：全部 / 白名单 / 黑名单，可按群单独设；主人永远放行；私聊不管。

func sendersOf(mode string, users ...string) LinkExpand {
	e := DefaultLinks().Expand
	e.Senders.Mode, e.Senders.Users = mode, users
	return e
}

func TestAllowsSenderTable(t *testing.T) {
	group := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "500001"}
	private := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "600001", Self: "700001"}
	cases := []struct {
		name  string
		e     LinkExpand
		conv  types.Conversation
		owner bool
		qq    string
		want  bool
	}{
		{"默认全部放行", DefaultLinks().Expand, group, false, "111", true},
		{"默认认不出号也放行", DefaultLinks().Expand, group, false, "", true},
		{"白名单：名单里", sendersOf(SendersWhitelist, "111"), group, false, "111", true},
		{"白名单：名单外", sendersOf(SendersWhitelist, "111"), group, false, "222", false},
		{"白名单：认不出号", sendersOf(SendersWhitelist, "111"), group, false, "", false},
		{"白名单：号带前导零不算", sendersOf(SendersWhitelist, "111"), group, false, "0111", false},
		{"白名单：号不是数字", sendersOf(SendersWhitelist, "111"), group, false, "abc", false},
		{"白名单：空名单谁都不放", sendersOf(SendersWhitelist), group, false, "111", false},
		{"白名单：主人永远放行", sendersOf(SendersWhitelist), group, true, "", true},
		{"黑名单：名单里", sendersOf(SendersBlacklist, "111"), group, false, "111", false},
		{"黑名单：名单外", sendersOf(SendersBlacklist, "111"), group, false, "222", true},
		{"黑名单：认不出号不拦", sendersOf(SendersBlacklist, "111"), group, false, "", true},
		{"黑名单：主人在名单里也放行", sendersOf(SendersBlacklist, "111"), group, true, "111", true},
		{"私聊不管名单", sendersOf(SendersWhitelist), private, false, "222", true},
	}
	for _, c := range cases {
		if got := c.e.AllowsSender(c.conv, c.owner, c.qq); got != c.want {
			t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.want)
		}
	}
}

func TestAllowsSenderGroupOverride(t *testing.T) {
	e := sendersOf(SendersBlacklist, "111")
	e.Senders.Groups = map[string]LinkSenderRule{
		"500001": {Mode: SendersWhitelist, Users: []string{"111"}},
		"500002": {Mode: SendersAll},
	}
	g := func(id string) types.Conversation {
		return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: id}
	}
	if !e.AllowsSender(g("500001"), false, "111") || e.AllowsSender(g("500001"), false, "222") {
		t.Fatal("单独设了白名单的群只放名单里的人（不和全局黑名单合并）")
	}
	if !e.AllowsSender(g("500002"), false, "111") {
		t.Fatal("单独设成 all 的群谁都放")
	}
	if e.AllowsSender(g("500003"), false, "111") || !e.AllowsSender(g("500003"), false, "222") {
		t.Fatal("没单独设的群用全局的黑名单")
	}
}

func TestLinkSendersCheck(t *testing.T) {
	if bad := DefaultLinks().Expand.Senders.check(); len(bad) != 0 {
		t.Fatalf("默认值应当合法：%v", bad)
	}
	many := make([]string, maxSenderUsers+1)
	for i := range many {
		many[i] = "1"
	}
	cases := map[string]LinkSenders{
		"模式写错":     {Mode: "allow"},
		"模式空着":     {Mode: ""},
		"号不是数字":    {Mode: SendersWhitelist, Users: []string{"abc"}},
		"号带前导零":    {Mode: SendersWhitelist, Users: []string{"0123"}},
		"号是 0":     {Mode: SendersWhitelist, Users: []string{"0"}},
		"名单太长":     {Mode: SendersWhitelist, Users: many},
		"群号不是数字":   {Mode: SendersAll, Groups: map[string]LinkSenderRule{"g1": {Mode: SendersAll}}},
		"群规则没写模式":  {Mode: SendersAll, Groups: map[string]LinkSenderRule{"500001": {Users: []string{"111"}}}},
		"群规则号不是数字": {Mode: SendersAll, Groups: map[string]LinkSenderRule{"500001": {Mode: SendersBlacklist, Users: []string{"x1"}}}},
	}
	for name, s := range cases {
		bad := s.check()
		if len(bad) == 0 || !strings.Contains(strings.Join(bad, "；"), "linkReader.expand.senders") {
			t.Errorf("%s：应当报错并指出配置键：%v", name, bad)
		}
	}
}

func TestLinkSendersCloneIsDeep(t *testing.T) {
	c := DefaultLinks()
	c.Expand.Senders = LinkSenders{Mode: SendersWhitelist, Users: []string{"111"}, Groups: map[string]LinkSenderRule{"500001": {Mode: SendersBlacklist, Users: []string{"222"}}}}
	d := c.Clone()
	d.Expand.Senders.Users[0] = "999"
	r := d.Expand.Senders.Groups["500001"]
	r.Users[0] = "999"
	d.Expand.Senders.Groups["500002"] = LinkSenderRule{Mode: SendersAll}
	if c.Expand.Senders.Users[0] != "111" || c.Expand.Senders.Groups["500001"].Users[0] != "222" || len(c.Expand.Senders.Groups) != 1 {
		t.Fatalf("Clone 要深拷贝名单和按群的规则：%+v", c.Expand.Senders)
	}
}

// 权限配置拼错键不能悄悄变成“全部放行”（复核 2026-11-13）：senders 子树的未知键拒绝加载
func TestLinkSendersRejectsUnknownKeys(t *testing.T) {
	head := "linkReader:\n  expand:\n    senders:\n"
	bad := map[string]string{
		"mode 拼成 mod":       head + "      mod: whitelist\n      users: [\"111\"]\n",
		"groups 拼成 group":   head + "      mode: all\n      group:\n        \"500001\": {mode: whitelist, users: [\"111\"]}\n",
		"群规则 users 拼成 user": head + "      mode: all\n      groups:\n        \"500001\": {mode: whitelist, user: [\"111\"]}\n",
		"senders 写成列表":      "linkReader:\n  expand:\n    senders: [whitelist]\n",
	}
	for name, src := range bad {
		_, err := Parse([]byte(src), "test.yml")
		if err == nil || (name != "senders 写成列表" && !strings.Contains(err.Error(), "linkReader.expand.senders")) { // 写成列表在类型解码那一步就被拒，报错里是行列号
			t.Errorf("%s：应当拒绝加载并指出配置键：%v", name, err)
		}
	}
	good := head + "      mode: whitelist\n      users: [\"111\"]\n      groups:\n        500001: {mode: blacklist, users: [222]}\n"
	c, err := Parse([]byte(good), "test.yml")
	if err != nil {
		t.Fatalf("写对的配置应当能加载（群号、号不加引号也行）：%v", err)
	}
	s := c.LinkReader.Expand.Senders
	if s.Mode != SendersWhitelist || s.Groups["500001"].Mode != SendersBlacklist || len(s.Groups["500001"].Users) != 1 || s.Groups["500001"].Users[0] != "222" {
		t.Fatalf("解析结果不对：%+v", s)
	}
	if c, err := Parse([]byte("linkReader:\n  expand:\n    enabled: true\n"), "test.yml"); err != nil || c.LinkReader.Expand.Senders.Mode != SendersAll {
		t.Fatalf("不写 senders 应当是默认 all：%v %+v", err, c.LinkReader.Expand.Senders)
	}
}
