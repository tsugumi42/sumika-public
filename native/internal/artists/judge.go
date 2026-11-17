package artists

// 按需判断的线索表和打分（规格-画师原图 第七节）。线索表是同目录的 clues.json（go:embed），确认页的生成脚本读同一份。
// 判断本身不发任何请求：输入是这一次取回来的作者资料和推文正文；一个线索一个账号只算一次。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed clues.json
var cluesJSON []byte

type clue struct {
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`
	Scope   string  `json:"scope"`
	Pattern string  `json:"pattern"`
	re      *regexp.Regexp
}

type ratioClue struct {
	Name        string  `json:"name"`
	MinRatio    float64 `json:"minRatio"`
	MinStatuses int64   `json:"minStatuses"`
	Weight      float64 `json:"weight"`
}

// Thresholds 是线索表里的阈值
type Thresholds struct {
	// AutoPending 是按需判断：总分达到它才记 pending
	AutoPending float64 `json:"autoPending"`
	// FollowingArtist、FollowingMaybe 是确认页对关注列表的分类线（只算 profile 线索）
	FollowingArtist float64 `json:"followingArtist"`
	FollowingMaybe  float64 `json:"followingMaybe"`
}

type table struct {
	Clues      []clue      `json:"clues"`
	Ratio      []ratioClue `json:"ratio"`
	Thresholds Thresholds  `json:"thresholds"`
}

var rules = mustLoad(cluesJSON)

func mustLoad(b []byte) table {
	var t table
	if err := json.Unmarshal(b, &t); err != nil {
		panic("artists: clues.json 读不开：" + err.Error())
	}
	for i := range t.Clues {
		c := &t.Clues[i]
		if c.Scope != "profile" && c.Scope != "tweet" {
			panic(fmt.Sprintf("artists: 线索 %q 的 scope 只能是 profile 或 tweet", c.Name))
		}
		re, err := regexp.Compile("(?i)" + c.Pattern)
		if err != nil {
			panic(fmt.Sprintf("artists: 线索 %q 的正则写错了：%v", c.Name, err))
		}
		c.re = re
	}
	return t
}

// DefaultThresholds 是线索表里的阈值
func DefaultThresholds() Thresholds { return rules.Thresholds }

// Profile 是判断用的作者资料。Known 为假（取数线路没给资料）时调用方不该来判断；
// 数字字段为负表示没给（比例线索不算）。字符串都是外来文字，只在这里用来匹配，不进日志。
type Profile struct {
	Name, Bio, Location string
	// Links 是简介、主页里展开后的真实地址
	Links                           []string
	MediaCount, Statuses, Followers int64
}

// Judgement 是判断结果：分数、命中的线索名（只写线索表里的名字，不写简介原文）
type Judgement struct {
	Score float64
	Clues []string
}

// Judge 给一个账号打分。tweetText 是这条推文的正文（只有 tweet 范围的线索看它）；确认页的关注列表没有推文，传空串。
func Judge(p Profile, tweetText string) Judgement {
	profile := strings.Join(append([]string{p.Name, p.Bio, p.Location}, p.Links...), "\n")
	var j Judgement
	hit := func(name string, w float64) {
		j.Clues = append(j.Clues, name)
		j.Score += w
	}
	for _, c := range rules.Clues {
		text := profile
		if c.Scope == "tweet" {
			text = tweetText
		}
		if text != "" && c.re.MatchString(text) {
			hit(c.Name, c.Weight)
		}
	}
	if p.Statuses > 0 && p.MediaCount >= 0 {
		ratio := float64(p.MediaCount) / float64(p.Statuses)
		for _, r := range rules.Ratio { // 表里从高档到低档排，只算第一个够得上的
			if p.Statuses >= r.MinStatuses && ratio >= r.MinRatio {
				hit(r.Name, r.Weight)
				break
			}
		}
	}
	return j
}

// FollowingClass 是确认页对关注列表的分类：画师 / 可能是 / 不是（只按 profile 线索，阈值见 clues.json）
func FollowingClass(score float64) string {
	switch {
	case score >= rules.Thresholds.FollowingArtist:
		return "画师"
	case score >= rules.Thresholds.FollowingMaybe:
		return "可能是"
	}
	return "不是"
}
