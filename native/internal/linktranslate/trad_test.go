package linktranslate

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// 繁体判断（用户 2026-11-13：不是简体中文就只放译文）：表驱动，覆盖纯简体、纯繁体、混排、日文汉字为主但有假名。
func TestTraditionalNeedsAndSourceLanguage(t *testing.T) {
	cases := []struct {
		name, text string
		need       bool
		lang       string
	}{
		{"纯简体", "今天的纸船试航很成功，大家都很开心，谢谢你们的关注。", false, "中文"},
		{"纯简体带个别共用字", "我们在这个时候开了会，后来还是决定过两天再说。", false, "中文"},
		{"纯繁体", "今天的紙船試航很成功，大家都很開心，謝謝你們的關注。", true, "繁体中文"},
		{"繁体短句", "我們這個週末去看展覽", true, "繁体中文"},
		{"繁体夹英文名", "Artemis 任務發射成功，大家都為此感到驕傲", true, "繁体中文"},
		{"简体引用一个繁体词", "他把这个系统叫做「體驗」，据说这是整个团队讨论了很久之后才定下来的名字，我觉得很有意思。", false, "中文"},
		{"繁体里夹少量简体", "這次的活動由大家一起準備，场地和時間都已經確定，請準時到場。", true, "繁体中文"},
		{"日文汉字为主但有假名", "本日の會議は中止です。次回は來週にします。", true, "日语"}, // 假名不少于两个：先按日语，Needs 本来就会翻
		{"日文汉字多只有两个假名", "本日の会議は中止です", true, "日语"},
		{"日文新字体汉字加假名", "今日はいい天気ですね。散歩に行ってきます。", true, "日语"},
		{"简体里一个繁体字", "这家店的招牌写着「愛」字，很多人都在门口拍照留念。", false, "中文"},
	}
	for _, c := range cases {
		got, _ := Needs(c.text, "简体中文")
		if got != c.need {
			t.Errorf("%s：Needs=%v，要 %v", c.name, got, c.need)
		}
		if lang := SourceLanguage(c.text); lang != c.lang {
			t.Errorf("%s：SourceLanguage=%q，要 %q", c.name, lang, c.lang)
		}
	}
}

// 目标本身是繁体时不因为“原文是繁体”去翻（保持原行为：中文汉字就算已是目标）。
func TestTraditionalTargetKeepsOldBehavior(t *testing.T) {
	for _, target := range []string{"繁體中文", "繁体中文", "Traditional Chinese", "zh-TW", "zh-Hant"} {
		if ok, why := Needs("今天的紙船試航很成功，大家都很開心。", target); ok || why != SkipAlreadyTarget {
			t.Errorf("目标 %q：Needs=%v %q", target, ok, why)
		}
	}
}

// 提示词要明说：目标是简体时繁体不算“已经是目标语言”，要换成简体字。
func TestPromptSaysTraditionalToSimplified(t *testing.T) {
	p := System(DefaultTarget)
	if !strings.Contains(p, "Traditional Chinese") || !strings.Contains(p, "NOT already in Simplified Chinese") {
		t.Fatalf("提示词没写繁体要转简体：%s", p)
	}
}

// 表本身：没有重复字、都是单个汉字（重复不影响判断，但说明抄错了）。
func TestTraditionalTableClean(t *testing.T) {
	seen := map[rune]bool{}
	for _, r := range traditionalOnly {
		if seen[r] {
			t.Errorf("繁体表里 %q 重复", r)
		}
		seen[r] = true
		if utf8.RuneLen(r) != 3 {
			t.Errorf("繁体表里 %q 不是汉字", r)
		}
	}
}

// 繁体推文会真的去翻译：Needs 放行、模型被调用、提示词要求转简体、译文原样回来；模型回哨兵（本来就是简体）才不翻。
func TestTraditionalTextIsTranslated(t *testing.T) {
	const post = "今天的紙船試航很成功，大家都很開心，謝謝你們的關注。"
	var sys string
	calls := 0
	tr := &Translator{Call: func(_ context.Context, system, user string) (string, error) {
		calls++
		sys = system
		return "今天的纸船试航很成功，大家都很开心，谢谢你们的关注。", nil
	}}
	out := tr.Translate(context.Background(), "https://example.invalid/x/1", post, DefaultTarget)
	if out.Status != StatusOK || out.Text != "今天的纸船试航很成功，大家都很开心，谢谢你们的关注。" || calls != 1 {
		t.Fatalf("繁体该翻：%+v 调用 %d 次", out, calls)
	}
	if !strings.Contains(sys, "Simplified Chinese") {
		t.Fatalf("系统提示词没写译成简体：%s", sys)
	}
	// 简体原文不调模型
	calls = 0
	out = tr.Translate(context.Background(), "https://example.invalid/x/2", "今天的纸船试航很成功，大家都很开心，谢谢你们的关注。", DefaultTarget)
	if out.Status != StatusSkipped || out.Reason != SkipAlreadyTarget || calls != 0 {
		t.Fatalf("简体不该翻：%+v 调用 %d 次", out, calls)
	}
}

// 2026-11-14：纯汉字的日文别标成“翻译自繁体中文”。日文新字体、和制汉字、日式词是日文特征；繁体独有字才证明是繁体；
// 只有和日文同形的字（個 為 開 時……）的，翻译照常放行（不是简体），但分不出是哪一种，写“外语”。
func TestPureHanJapaneseIsNotTraditional(t *testing.T) {
	cases := []struct {
		name, text string
		need       bool
		lang       string
	}{
		{"新字体：车站集合", "東京駅前集合", true, "日语"},
		{"新字体：发售", "本日発売", true, "日语"},
		{"共用字加新字体", "開催時間変更", true, "日语"},
		{"和制汉字", "新刊の頒布を明日に変更", true, "日语"},
		{"日式词加共用字", "個人開催", true, "日语"},
		{"日式词公告体", "既刊通販受付中", true, "日语"},
		{"纯繁体口语", "這個問題還沒解決", true, "繁体中文"},
		{"纯繁体长句", "今天的紙船試航很成功，大家都很開心，謝謝你們的關注。", true, "繁体中文"},
		{"纯简体", "这个问题还没解决", false, "中文"},
		{"简体里夹一个日文站名", "今天我们在新宿駅碰面，然后一起去看了很久没见的朋友办的展览，大家玩得很开心。", false, "中文"},
		{"只有共用字：不是简体，分不出", "個過開時", true, ""},
	}
	for _, c := range cases {
		got, _ := Needs(c.text, "简体中文")
		if got != c.need {
			t.Errorf("%s：Needs=%v，要 %v", c.name, got, c.need)
		}
		if lang := SourceLanguage(c.text); lang != c.lang {
			t.Errorf("%s：SourceLanguage=%q，要 %q", c.name, lang, c.lang)
		}
	}
}

// 三张字表两两不相交、都是单个汉字、没有重复字；日文特征表里不混进简体文字会写的字。
func TestScriptTablesDisjoint(t *testing.T) {
	tables := map[string]string{"traditionalOnly": traditionalOnly, "sharedWithJapanese": sharedWithJapanese, "japaneseOnly": japaneseOnly}
	owner := map[rune]string{}
	for name, tbl := range tables {
		seen := map[rune]bool{}
		for _, r := range tbl {
			if seen[r] {
				t.Errorf("%s 里 %q 重复", name, r)
			}
			seen[r] = true
			if utf8.RuneLen(r) != 3 {
				t.Errorf("%s 里 %q 不是汉字", name, r)
			}
			if other, ok := owner[r]; ok {
				t.Errorf("%q 同时在 %s 和 %s", r, other, name)
			}
			owner[r] = name
		}
	}
	// 简体文字里常用、日文新字体里也有的字不能进日文特征表（否则简体会被判成日语）
	for _, r := range "国学会体点来党双当写画区个为后开时还过这说们" {
		if owner[r] == "japaneseOnly" {
			t.Errorf("%q 简体里也用，不能放进 japaneseOnly", r)
		}
	}
	for _, sample := range []string{
		"今天的纸船试航很成功，大家都很开心，谢谢你们的关注。", "我们在这个时候开了会，后来还是决定过两天再说。",
		"国家发展改革委员会发布了新的政策，经济增长保持稳定，各地区的发展都取得了明显进展。",
		"他买了一张去上海的火车票，在车站等了很久，终于见到了多年没见的老朋友。",
	} {
		for _, r := range sample {
			if owner[r] != "" {
				t.Errorf("简体句子里的 %q 在 %s 里：%q", r, owner[r], sample)
			}
		}
	}
	for _, w := range japaneseWords {
		for _, r := range w {
			if owner[r] == "traditionalOnly" {
				t.Errorf("日式词 %q 里的 %q 是繁体独有字", w, r)
			}
		}
	}
}

// 日文没有假名时的判断只在汉字文字里生效：有韩文、拉丁字母为主的不受影响；日文特征字占比太低（简体长文夹一个）不判日语。
func TestJapaneseFeatureDoesNotLeak(t *testing.T) {
	if lang := SourceLanguage("I took the 駅 line to the museum with my friends yesterday afternoon"); lang != "英语" {
		t.Errorf("英文夹一个日文字：%q", lang)
	}
	if lang := SourceLanguage("지하철 駅 근처에서 만나요"); lang != "韩语" {
		t.Errorf("韩文夹一个日文字：%q", lang)
	}
}
