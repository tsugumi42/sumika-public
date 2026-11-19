package memory

import (
	"context"
	"strings"
	"testing"
)

// 写前密钥形状过滤（规格-记忆写入上线 W5 验收 8）。下面的"密钥"都是编的，不是真的

// fakeSK 造一个编的 sk- 形状的串：n 个字母数字。拆开拼，免得这个文件里出现整串像密钥的字面量
func fakeSK(n int) string {
	return "sk" + "-" + strings.Repeat("abcdefghij", 3)[:n]
}

func TestSecretShaped(t *testing.T) {
	t.Parallel()
	hex32 := strings.Repeat("a1b2c3d4", 4)
	cases := []struct {
		text string
		want bool
	}{
		// sk-：后面 20 位以上
		{"我的 key 是 " + fakeSK(20) + " 别告诉别人", true},
		{fakeSK(19), false},
		{"sk" + "-proj-abcdefghij_klmnopqrst", true},
		{"这个 task-force 很大", false},
		// 十六进制：32 位以上
		{hex32, true},
		{hex32[:31], false},
		{"前缀" + strings.ToUpper(hex32) + "后缀", true},
		{hex32 + "0", true},
		{"zzzz" + hex32[:20] + "zzzz" + hex32[:20], false}, // 被打断，各 20 位
		// AKIA + 16 位大写字母数字
		{"AKI" + "AABCDEFGHIJKLMNOP", true}, // 拼出来：源码里直接写会被隐私扫描当成 AWS 密钥拦下
		{"AKIAABCDEFGHIJKLMNO", false},
		{"akiaabcdefghijklmnop", false},
		// 关键词后面 6 位以上连续非空白
		{"我的密码是123456", true},
		{"密码：abcdef", true},
		{"密码：abcde", false},
		{"密码 abcdef", true},
		{"password: abcdef", true},
		{"PassWord=abcdef1", true},
		{"password: abc", false},
		{"token abcdefg", true},
		{"token abc def", false},
		{"这是密钥abcdefgh", true},
		{"密钥很重要", false},
		// 普通的话
		{"主人喜欢吃橘子", false},
		{"主人的生日是 3 月 3 日", false},
		{"今天去了公园，天气很好", false},
		{"", false},
	}
	for _, c := range cases {
		if got := secretShaped(c.text); got != c.want {
			t.Errorf("%q：得到 %v，应该是 %v", c.text, got, c.want)
		}
	}
}

// 端到端：整理出来的事实和片段里有密钥形状的不写，没有的照写；日志只有计数
func TestSecretDropped(t *testing.T) {
	t.Parallel()
	secret := fakeSK(26)
	r := newRig(t, "qq:private:20001:60001")
	r.say(ownerA, "我有个新的 key")
	r.say(ownerA, "今天去了公园")
	r.reply("yes", "主人贴了一个 key："+secret+"，又去了公园",
		cand("主人的 key 是 "+secret, "owner", 1),
		cand("主人今天去了公园", "owner", 2),
		cand("主人的密码是hunter2hunter", "owner", 1))
	res := r.run()
	if res.Skipped != "" {
		t.Fatalf("该整理了：%q", res.Skipped)
	}
	if res.Counts[CountSecretDropped] != 3 {
		t.Fatalf("两条事实一个片段含密钥形状该丢 3 次：%v", res.Counts)
	}
	if got := r.factTexts(); len(got) != 1 || got[0] != "主人今天去了公园" {
		t.Fatalf("库里该只有没有密钥的那条：%v", got)
	}
	if eps := r.episodes(); len(eps) != 0 {
		t.Fatalf("含密钥形状的片段不该写：%v", eps)
	}
	log := r.sink.String()
	if !strings.Contains(log, "secret_dropped=3") {
		t.Errorf("日志该有计数：%q", log)
	}
	for _, leak := range []string{secret, "hunter2", "公园"} {
		if strings.Contains(log, leak) {
			t.Errorf("日志里有正文 %q：%s", leak, log)
		}
	}
	// 游标照常推过去，不因为丢了东西就卡在这一批
	if r.cursor() != 2 {
		t.Errorf("游标该推到 2：%d", r.cursor())
	}

	// 没有密钥的片段照写
	r.say(ownerA, "晚上吃了面")
	r.reply("yes", "主人晚上吃了面", cand("主人晚上吃了面", "owner", 3))
	if res := r.run(); res.Counts[CountSecretDropped] != 0 {
		t.Fatalf("没有密钥形状不该丢：%v", res.Counts)
	}
	if eps := r.episodes(); len(eps) != 1 || !strings.Contains(eps[0], "面") {
		t.Fatalf("普通片段该写：%v", eps)
	}
}

// remember 是主人亲口让她记的，不过这道过滤
func TestRememberIsNotFilteredForSecrets(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	const text = "我家 wifi 的密码是abcd1234efgh"
	mustApply(t, m, remember(ownerFact(text)))
	if n := count(t, m, `SELECT count(*) FROM facts WHERE text = ?`, text); n != 1 {
		t.Fatalf("remember 该照写：%d", n)
	}
	_ = context.Background()
}
