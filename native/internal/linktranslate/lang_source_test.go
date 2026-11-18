package linktranslate

import "testing"

// 推特卡片的“翻译自……”要认出原文的语种：日语、韩语、英语、中文认得出；认不出（别的拉丁字母语种、西里尔字母）是空串，
// 调用方写“外语”，不把法语标成英语。
func TestSourceLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"冬コミ受かりました！スペースは1日目の東1ホールです":                                     "日语",
		"치이/커미션 Momoi 🤍 Midori 신청 감사합니다":                                 "韩语",
		"This is the first time I have seen the paper boat float":        "英语",
		"I'm just not sure that it's the right time for this":            "英语",
		"今天的纸船试航很成功，大家都很开心":                                              "中文",
		"Le bateau en papier a flotté toute la journée, c'est très beau": "",
		"El barco de papel flotó todo el día y fue muy bonito":           "",
		"Бумажный кораблик плавал весь день":                             "",
		"https://example.invalid/x @someone #tag":                        "",
		"":   "",
		"ok": "",
		// 日文里夹了几个汉字也是日语；只有假名一个字不算（和 Needs 同一个口径）
		"紙の舟、試航成功！みんなありがとう": "日语",
	} {
		if got := SourceLanguage(in); got != want {
			t.Errorf("%q → %q，要 %q", in, got, want)
		}
	}
}
