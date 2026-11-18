package artists

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// 线索表里每条线索各有一个命中例、一个不命中例（规格-画师原图 验收 9）。例子都是编的。
func TestEveryClueHasHitAndMiss(t *testing.T) {
	type pair struct{ hit, miss string }
	profile := map[string]pair{
		"自称绘师":           {"絵描きです", "普通の会社員です"},
		"pixiv链接":        {"https://www.pixiv.net/users/1", "https://example.invalid/pixiv"},
		"skeb链接":         {"https://skeb.jp/@paper", "https://example.invalid/skeb"},
		"fanbox链接":       {"https://paper.fanbox.cc/", "https://example.invalid/fan"},
		"イラスト":           {"イラストを載せます", "写真を載せます"},
		"fantia或booth链接": {"https://booth.pm/ja/items/1", "https://example.invalid/booth"},
		"接稿":             {"お仕事のご依頼はDMへ", "雑談はDMへ"},
		"漫画同人":           {"同人誌を作っています", "ゲーム実況をしています"},
		"二次创作或原创":        {"ファンアートも描きます", "旅行の記録です"},
		"描く":             {"絵を描く", "写真を撮る"},
		"patreon":        {"https://patreon.com/paper", "https://example.invalid/pat"},
		// 第 2 版（2026-11-13，按确认页 v1～v5 的标注加的）
		"展会摊位":     {"C107 1日目 東ア-12a", "東京在住です"},
		"同人サークル":   {"サークル「紙船」で活動中", "趣味は散歩"},
		"防转载反AI声明": {"無断転載禁止", "転職しました"},
		"R18声明":    {"18歳未満の方はフォローご遠慮ください", "18時から配信します"},
		"画画":       {"お絵かきが好き", "絵本を読む"},
		"作品站点其他":   {"https://www.artstation.com/paper", "https://example.invalid/art"},
		"插画工作":     {"ラノベの挿絵を担当しました", "小説を読む"},
		"游戏开发":     {"indie dev making a cozy game", "art community member"},
		"官方运营":     {"【公式】紙船プロジェクト", "公式info @paper_info"},
		"Coser":    {"コスプレしてます", "コスメが好き"},
		"配音":       {"声優をしています", "声が好き"},
		"音乐":       {"作曲とDTMをやってます", "作品を載せます"},
		"摄影":       {"photographer based in Tokyo", "photo diary"},
	}
	for _, c := range rules.Clues {
		if c.Scope == "tweet" {
			continue
		}
		p, ok := profile[c.Name]
		if !ok {
			t.Errorf("线索 %q 没有测试例", c.Name)
			continue
		}
		if !slices.Contains(Judge(Profile{Bio: p.hit, MediaCount: -1}, "").Clues, c.Name) {
			t.Errorf("线索 %q 应该命中 %q", c.Name, p.hit)
		}
		if slices.Contains(Judge(Profile{Bio: p.miss, MediaCount: -1}, "").Clues, c.Name) {
			t.Errorf("线索 %q 不该命中 %q", c.Name, p.miss)
		}
		// 名字、地点里的线索也算
		if !slices.Contains(Judge(Profile{Name: p.hit, MediaCount: -1}, "").Clues, c.Name) || !slices.Contains(Judge(Profile{Location: p.hit, MediaCount: -1}, "").Clues, c.Name) {
			t.Errorf("线索 %q 在名字、地点里也该命中", c.Name)
		}
		if !slices.Contains(Judge(Profile{Links: []string{p.hit}, MediaCount: -1}, "").Clues, c.Name) {
			t.Errorf("线索 %q 在展开后的链接里也该命中", c.Name)
		}
	}
	// 推文线索
	if !slices.Contains(Judge(Profile{MediaCount: -1}, "今日の落書き #イラスト").Clues, "推文画师标签") {
		t.Error("推文标签应该命中")
	}
	if slices.Contains(Judge(Profile{MediaCount: -1}, "今日のごはん #グルメ").Clues, "推文画师标签") {
		t.Error("推文标签不该命中")
	}
	// tweet 范围的线索不看简介，profile 范围的线索不看推文
	if slices.Contains(Judge(Profile{Bio: "#イラスト", MediaCount: -1}, "").Clues, "推文画师标签") {
		t.Error("推文标签不该看简介")
	}
	if slices.Contains(Judge(Profile{MediaCount: -1}, "絵描きです").Clues, "自称绘师") {
		t.Error("简介线索不该看推文正文")
	}
}

func TestDrawVerbIsWeak(t *testing.T) {
	j := Judge(Profile{Bio: "絵を描く", MediaCount: -1}, "")
	if j.Score != 0.5 {
		t.Fatalf("“描く”只该加 0.5，得到 %v（%v）", j.Score, j.Clues)
	}
	if FollowingClass(j.Score) != "不是" {
		t.Fatal("只有“描く”不该算可能是")
	}
}

func TestPatreonDoesNotScore(t *testing.T) {
	j := Judge(Profile{Bio: "https://patreon.com/paper", MediaCount: -1}, "")
	if j.Score != 0 || len(j.Clues) != 1 {
		t.Fatalf("patreon 只显示不计分：%+v", j)
	}
}

func TestEachClueCountsOnce(t *testing.T) {
	once := Judge(Profile{Bio: "絵師", MediaCount: -1}, "")
	many := Judge(Profile{Bio: "絵師 絵師 画师 画师 illustrator", MediaCount: -1}, "")
	if once.Score != many.Score || once.Score != 3 {
		t.Fatalf("一个线索只算一次：%v %v", once.Score, many.Score)
	}
}

func TestRatioClue(t *testing.T) {
	cases := []struct {
		media, statuses int64
		want            float64
		name            string
	}{
		{700, 1000, 1.5, "带图比例高"},
		{600, 1000, 1.5, "带图比例高"}, // 刚好 0.6
		{599, 1000, 1, "带图比例偏高"},
		{300, 1000, 1, "带图比例偏高"},
		{299, 1000, 0, ""},
		{99, 99, 1, "带图比例偏高"}, // statuses<100 不算“高”，但够“偏高”
		{20, 49, 0, ""},       // statuses<50 都不算
		{0, 1000, 0, ""},
		{-1, 1000, 0, ""}, // 没给
		{500, 0, 0, ""},   // 除零
		{5000, 100, 1.5, "带图比例高"},
	}
	for _, c := range cases {
		j := Judge(Profile{MediaCount: c.media, Statuses: c.statuses}, "")
		if j.Score != c.want || (c.name != "" && !slices.Contains(j.Clues, c.name)) || (c.name == "" && len(j.Clues) != 0) {
			t.Errorf("%d/%d：得到 %+v，想要 %v %q", c.media, c.statuses, j, c.want, c.name)
		}
	}
}

func TestZeroValueProfileScoresZero(t *testing.T) {
	// 数字字段零值（0/0）等于没给：不加比例线索
	if j := Judge(Profile{}, ""); j.Score != 0 || len(j.Clues) != 0 {
		t.Fatalf("%+v", j)
	}
}

func TestAutoThreshold(t *testing.T) {
	th := DefaultThresholds()
	if th.AutoPending != 3 || th.FollowingArtist != 4 || th.FollowingMaybe != 2 {
		t.Fatalf("阈值和规格不一致：%+v", th)
	}
	// 没有任何简介线索，但带图比例高 + 推文带标签：1.5 + 1.5 = 3，够线
	j := Judge(Profile{MediaCount: 800, Statuses: 1000}, "新作です #illustration")
	if j.Score < th.AutoPending {
		t.Fatalf("无简介线索的画师应该能靠比例加标签够线：%+v", j)
	}
	// 只有比例，不够线
	if j := Judge(Profile{MediaCount: 800, Statuses: 1000}, "ただの写真"); j.Score >= th.AutoPending {
		t.Fatalf("只靠比例不该够线：%+v", j)
	}
}

func TestFollowingClass(t *testing.T) {
	for score, want := range map[float64]string{0: "不是", 1.5: "不是", 1.9: "不是", 2: "可能是", 3.5: "可能是", 4: "画师", 9: "画师"} {
		if got := FollowingClass(score); got != want {
			t.Errorf("%v → %q，想要 %q", score, got, want)
		}
	}
}

// clue-samples.json 是 Go 和确认页脚本共用的样例：同一个简介两边得分、命中线索必须一样。确认页脚本已移出仓库，放在本机 data/dev/tools/artist-review.ts（不进 git，ADR-389）。
func TestClueSamplesMatchFile(t *testing.T) {
	b, err := os.ReadFile("testdata/clue-samples.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Name       string   `json:"name"`
		Bio        string   `json:"bio"`
		Location   string   `json:"location"`
		Links      []string `json:"links"`
		MediaCount int64    `json:"mediaCount"`
		Statuses   int64    `json:"statuses"`
		Tweet      string   `json:"tweet"`
		Score      float64  `json:"score"`
		Clues      []string `json:"clues"`
	}
	if err := json.Unmarshal(b, &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) < 8 {
		t.Fatal("样例太少")
	}
	for i, s := range samples {
		j := Judge(Profile{Name: s.Name, Bio: s.Bio, Location: s.Location, Links: s.Links, MediaCount: s.MediaCount, Statuses: s.Statuses}, s.Tweet)
		if j.Score != s.Score || !slices.Equal(j.Clues, s.Clues) {
			t.Errorf("样例 %d：得到 %v %v，文件里是 %v %v", i, j.Score, j.Clues, s.Score, s.Clues)
		}
	}
}
