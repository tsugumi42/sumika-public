package kernel

import (
	"strings"
	"testing"
)

// runes 用码点拼字符串：不可见字符写成码点，别在源码里放原字符，也不靠转义。
func runes(rs ...rune) string { return string(rs) }

// 归一和"要不要有账号"的单元表。形状 × 入口的整张表在 turn 包（input_selfactor_test.go），这里只钉函数本身的口径。
func TestAccountCanonAndSelf(t *testing.T) {
	t.Parallel()
	qq := Scope{Platform: "qq", Channel: "qq"}
	tg := Scope{Platform: "tgplat", Channel: "tg"} // 渠道名和平台名不同的虚构平台
	for _, c := range []struct {
		name     string
		scope    Scope
		account  string
		actor    string
		wantSelf bool
	}{
		{"裸号相同", qq, "101", "101", true},
		{"发言人带渠道前缀", qq, "101", "qq:101", true},
		{"账号带渠道前缀", qq, "qq:101", "101", true},
		{"两边都带前缀", qq, "qq:101", "qq:101", true},
		{"大写前缀", qq, "101", "QQ:101", true},
		{"前后空白", qq, " 101\t", "101", true},
		{"夹空白", qq, "10 1", "101", true},
		{"零宽字符", qq, "101", "1\u200b01", true},
		{"全角数字", qq, "101", "１０１", true},
		{"叠前缀", qq, "101", "qq:qq:101", true},
		{"平台前缀（渠道名和平台名不同）", tg, "101", "tgplat:101", true},
		{"渠道前缀（渠道名和平台名不同）", tg, "101", "tg:101", true},
		{"别的渠道的前缀不剥", qq, "101", "tg:101", false},
		{"另一个号", qq, "101", "qq:102", false},
		{"号只是前缀相同", qq, "101", "1011", false},
		{"账号空不算自己", qq, "", "", false},
		{"变体选择符 U+FE0F", qq, "123", "123" + runes(0xfe0f), true},
		{"组合用连字符 CGJ U+034F", qq, "123", "123" + runes(0x34f), true},
		{"韩文填充 U+3164", qq, "123", "123" + runes(0x3164), true},
		{"盲文空白 U+2800", qq, "123", runes(0x2800) + "123", true},
		{"变体选择符补充区 U+E0100", qq, "123", "1" + runes(0xe0100) + "23", true},
		{"丢掉零宽连接符后才能组合（é 对 e+ZWJ+U+0301）", qq, runes(0xe9), "e" + runes(0x200d, 0x301), true},
		{"反过来", qq, "e" + runes(0x200d, 0x301), runes(0xe9), true},
		{"é 对 e（少了组合符，是另一个字）", qq, runes(0xe9), "e", false},
		{"账号和发言人是同一串空白（原样相同，旧代码拒）", qq, " ", " ", true},
		{"账号和发言人都只有渠道前缀（原样相同）", qq, "qq:", "qq:", true},
		{"账号和发言人是同一个零宽字符（原样相同）", qq, "\u200b", "\u200b", true},
		{"账号只有空白不算自己", qq, " ", "", false},
		{"占位值 channel 对上自己", qq, "channel", "qq:channel", true},
		{"占位值 channel 对别人", qq, "channel", "qq:103", false},
		{"发言人是 owner、账号是 desktop", Scope{Platform: "local", Channel: "console"}, "desktop", "owner", false},
	} {
		s := c.scope
		s.AccountID, s.ActorID = c.account, c.actor
		if got := s.ActorIsAccount(); got != c.wantSelf {
			t.Errorf("%s：ActorIsAccount=%v 想要 %v", c.name, got, c.wantSelf)
		}
	}
}

func TestAccountRequired(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name              string
		platform, channel string
		want              bool
	}{
		{"本地主窗口", "local", "console", false},
		{"QQ", "qq", "qq", true},
		{"没登记的新平台", "telegram", "telegram", true},
		{"平台是 local、渠道不是 console", "local", "qq", true},
		{"渠道是 console、平台不是 local", "qq", "console", true},
		{"平台空", "", "console", true},
		{"渠道空", "local", "", true},
		{"大小写不同的 Local 不算本地", "Local", "console", true},
	} {
		s := Scope{Platform: c.platform, Channel: c.channel}
		if got := s.AccountRequired(); got != c.want {
			t.Errorf("%s：AccountRequired=%v 想要 %v", c.name, got, c.want)
		}
		// 缺账号 = 要账号且账号归一后是空
		for _, acct := range []string{"", " ", "\u200b", "\u00a0"} {
			s.AccountID = acct
			if got := s.AccountMissing(); got != c.want {
				t.Errorf("%s：账号 %q AccountMissing=%v 想要 %v", c.name, acct, got, c.want)
			}
		}
		s.AccountID = "1"
		if s.AccountMissing() {
			t.Errorf("%s：有账号还报缺", c.name)
		}
	}
}

// 归一要幂等：归一两遍等于归一一遍。碎片里放了会"丢掉以后又能组合"的组合（零宽连接符隔开的 e 和 U+0301）、
// 变体选择符、CGJ、韩文填充、盲文空白、全角、会展开的连字、大小写特殊字符，三段以内全排列。
func TestCanonIdempotent(t *testing.T) {
	t.Parallel()
	frags := []string{"e", runes(0xe9), runes(0x301), runes(0x200d), runes(0xfe0f), runes(0x34f), runes(0x3164), runes(0x2800),
		"1", "１", "q", "Q", ":", " ", runes(0x1c5), runes(0x130), runes(0xfb03), runes(0xe0100), runes(0x115f), runes(0xffa0)}
	scopes := []Scope{{Platform: "qq", Channel: "qq"}, {Platform: "tgplat", Channel: "tg"}, {}}
	var all []string
	all = append(all, "")
	for _, a := range frags {
		all = append(all, a)
		for _, b := range frags {
			all = append(all, a+b)
			for _, c := range frags {
				all = append(all, a+b+c)
			}
		}
	}
	// 带前缀的写法也排一遍
	n := len(all)
	for i := 0; i < n; i += 7 {
		all = append(all, "qq:"+all[i], "QQ:"+runes(0x200d)+all[i], "tg:"+"tgplat:"+all[i])
	}
	for _, s := range scopes {
		for _, x := range all {
			once := s.CanonID(x)
			if twice := s.CanonID(once); twice != once {
				t.Fatalf("不幂等：scope=%+v x=%q 一遍=%q 两遍=%q", s, x, once, twice)
			}
			if strings.ContainsFunc(once, ignorableInID) {
				t.Fatalf("归一结果里还有该丢的字符：x=%q 结果=%q", x, once)
			}
		}
	}
}
