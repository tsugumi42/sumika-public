package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

func cardOf(t *testing.T, r linkreader.Result, site string) (author, date string, semantic string) {
	t.Helper()
	d := Build(&r, "", Options{Templates: map[string]string{site: "@卡片"}}, 0)
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Card != nil {
				return it.Card.Author, it.Card.DateDuration, it.Card.Semantic()
			}
		}
	}
	t.Fatalf("没有卡片：%v", d.Describe())
	return
}

// 卡片和文字版同一个字段同一种写法（用户 2026-11-13 复核 #68、新 4-1、#17、#2）：
// GitHub 的日期是“更新：”加更新时间，Steam 是“发行：”加发行日期，多 P 视频写总长而不是 P1 的时长。
func TestCardDatesMatchText(t *testing.T) {
	gh := caseNamed(t, "github")
	gh.GitHub.Updated = av(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	gh.Published = av(time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC)) // 创建时间，卡片不该拿它冒充更新时间
	if _, date, _ := cardOf(t, gh, "github"); !strings.HasPrefix(date, "更新：") || strings.Contains(date, "2025") {
		t.Errorf("GitHub 卡片日期：%q", date)
	}
	steam := siteResult("steam", "sharedfile", "90023")
	steam.Title = av("纸船模组")
	steam.Published = av(time.Date(2026, 9, 24, 0, 27, 0, 0, time.UTC))
	if _, date, _ := cardOf(t, steam, "steam"); date != "发行：2026-09-24" {
		t.Errorf("Steam 卡片日期：%q", date)
	}
	multi := goldenBili(3)
	multi.SelectedPart = linkreader.Field[linkreader.VideoPart]{State: "not_returned"} // 没指定第几 P
	if _, date, _ := cardOf(t, multi, "bilibili-video"); !strings.Contains(date, "总长：06:00") || strings.Contains(date, "时长：") {
		t.Errorf("多 P 卡片应写总长：%q", date)
	}
	single := goldenBili(1)
	if _, date, _ := cardOf(t, single, "bilibili-video"); !strings.Contains(date, "时长：") {
		t.Errorf("单 P 卡片仍写时长：%q", date)
	}
}

// 卡片上不带平台名前缀，没有的字段不留空行（用户 2026-11-13 复核“卡片残留”）。
func TestCardNoPlatformPrefixNoBlankLines(t *testing.T) {
	gh := caseNamed(t, "github")
	author, _, sem := cardOf(t, gh, "github")
	if author != "" || strings.Contains(sem, "GitHub") || strings.Contains(sem, "\n\n") || strings.HasSuffix(sem, "\n") {
		t.Errorf("GitHub 卡片：author=%q 语义=%q", author, sem)
	}
	space := siteResult("bilibili", "user", "90024")
	space.Title = av("纸灯实验室")
	space.Text = av("做折纸的小实验室")
	space.Stats["followers"] = av(int64(100))
	if author, _, sem := cardOf(t, space, "bilibili-space"); author != "" || strings.Contains(sem, "B站个人主页") || strings.HasSuffix(sem, "\n") {
		t.Errorf("主页卡片：author=%q 语义=%q", author, sem)
	}
}
