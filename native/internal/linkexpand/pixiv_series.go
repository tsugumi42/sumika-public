package linkexpand

import (
	"fmt"
	"strconv"

	"sumika-native/internal/linkreader"
)

// pixiv 系列：作品和小说页里的“系列：标题 第N话”、上一话和下一话，以及系列页（漫画系列、小说系列）的字段。

func orderText(f linkreader.Field[int64]) string {
	if f.State == "available" {
		return "第" + strconv.FormatInt(f.Value, 10) + "话"
	}
	return ""
}

// navLink 写成“第N话 标题 链接”；缺的项不写。
func navLink(s *linkreader.SeriesInfo, l *linkreader.SeriesLink) val {
	if l == nil {
		return none
	}
	text := orderText(l.Order)
	if t := optText(l.Title); t.ok {
		if text != "" {
			text += " "
		}
		text += t.text
	}
	if u := s.WorkURL(l.ID); u != "" {
		if text != "" {
			text += " "
		}
		text += u
	}
	if text == "" {
		return none
	}
	return some(text)
}

// pixivNavFields 是作品页和小说页的系列导航字段（系列、上一话、下一话）。
func pixivNavFields(r *linkreader.Result, set func(string, val)) {
	s := r.Series
	if s == nil {
		return
	}
	title := optText(s.Title)
	order := orderText(s.Order)
	switch {
	case title.ok && order != "":
		set("series", some(title.text+" "+order))
	case title.ok:
		set("series", title)
	case order != "":
		set("series", some(order))
	}
	set("prev", navLink(s, s.Prev))
	set("next", navLink(s, s.Next))
}

// pixivSeriesFields 是系列页的字段。
func pixivSeriesFields(r *linkreader.Result, set func(string, val)) {
	set("site", some("pixiv 系列"))
	set("title", optText(r.Title))
	set("author", optText(r.Author.Name))
	set("desc", optText(r.Text))
	if x, ok := flagBool(r, "ai_generated"); ok {
		set("ai", some(map[bool]string{true: "是", false: "否"}[x]))
	}
	if level, ok := Restriction(r); ok {
		set("rating", some([]string{"全年龄", "R18", "R18-G"}[level]))
	}
	set("firstline", pixivFirstLine(r)) // 同作品的首行：“[AI]🔞 | 标题 | 作者名”（用户 2026-11-12）
	// 类型按链接的种类定，不依赖 Series 有没有取到
	unit := "话"
	set("type", some("漫画系列"))
	if r.Kind == "novel_series" {
		unit = "篇"
		set("type", some("小说系列"))
	}
	s := r.Series
	if s == nil {
		return
	}
	if s.Total.State == "available" {
		set("count", some(fmt.Sprintf("共%d%s", s.Total.Value, unit)))
	}
	if u := s.WorkURL(s.LatestID); u != "" {
		set("latest", some(u))
	}
	if u := s.WorkURL(s.FirstID); u != "" {
		set("first", some(u))
	}
}
