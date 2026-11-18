package main

// 自检（报告 F 节，规格-记忆写入上线 W6 报告 F）：越权矩阵，和"派生内容不能比证据宽"。
//
// 越权矩阵：这次写出来的每条事实和片段 × 一组听众（本地私聊、本地群聊、QQ 主人的私聊、QQ 别人的私聊、别的 QQ 群、别的群里主人本人问起，
// 再加来源自己的那个场合），两边各算一遍"这个听众能不能看到这条"：
//   - 预期：expect.go 里手写的表（来源场合 → 范围 → 听众能看哪些范围），不调用被测的 CanSee；
//   - 实算：库里存下的真实范围，过 memory.CanSee(scopes, memory.AllowedScopes(听众))。
//
// 实算能看、预期不能看 = 越权（硬条件，必须是 0）；预期能看、实算看不到 = 漏看（多半是模型选了 private 收紧，只记数）。
// 来源场合取聊天库里的真实记录认的（classifyConv、对方是谁看会话里第一个 peer），不用整理时推断的 ownerPrivate 和 partner，
// 否则在本地私聊上永远为真，等于没测。
//
// "派生内容不能比证据宽"：每条事实的范围里，只要有一个值不在预期集合里（比如多了 public、多了 owner、多了别的会话），就记一条"范围比表宽"，
// 这条比矩阵更严：矩阵只看某几个听众，这条直接比范围本身。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"sumika-native/internal/audience"
	"sumika-native/internal/memory"
)

// finding 是自检里的一条发现（只有编号和听众名，没有正文）
type finding struct {
	Ref      string
	Audience string
	Kind     string // overreach（越权）/ missed（漏看）/ wider（范围比表宽）
	Actual   bool   // 实算能不能看到（wider 无意义）
}

type checkResult struct {
	Facts, Episodes int
	Cells           int // 事实 / 片段 × 听众 的格数
	Overreach       int // 实算能看、预期不能看
	Missed          int // 预期能看、实算看不到
	Wider           int // 范围里有不在预期集合里的值
	Unjudged        []string
	OwnerFromPeer   int // Q8：别人讲主人的事、范围进了 owner（现行规则，主人定了不改，ADR-232）
	// ProtectedChanged：跑之前有效、受保护的事实（W2：整理动不了它们），跑完状态或 confirmed_by 变了的编号。硬条件，必须是空的
	ProtectedChanged []string
	Findings         []finding
}

// auditAud 是矩阵里的一个听众：手写的预期一份，真实的 Audience 一份
type auditAud struct {
	Name string
	Hand handAud
	Real memory.Audience
}

// 虚构的、不会和真实会话撞上的会话 id
const (
	shadowConsoleGroup = "console:group:shadow-check"
	shadowOwnerPrivate = "qq:private:shadow-self:shadow-owner"
	shadowOtherPrivate = "qq:private:shadow-self:shadow-other"
	shadowOtherGroup   = "qq:group:shadow-other"
	shadowStranger     = "qq:shadow-z"
)

// auditAudiences 列出对一个来源（会话 conv、场合 k、私聊对方 peer）要检查的听众。
// 真实的 Audience 照 internal/channel/audience.go 的 AudienceOf 构造：私聊 PersonIDs 是对方、主人的私聊 PrivateOK 和 OwnerAsked 为真，
// 群里 PrivateOK 只有本地群为真、OwnerAsked 看触发那几条是不是主人发的
func auditAudiences(conv string, k convKind, peer string) []auditAud {
	owner := "owner"
	out := []auditAud{
		{"本地私聊（主窗口）", handAud{All: true}, audience.Console("console:shadow-main")},
		{"本地群聊", handAud{Conv: shadowConsoleGroup, Owner: true},
			memory.Audience{Channel: "console", Conversation: shadowConsoleGroup, Scene: "group", PrivateOK: true, PersonIDs: []string{owner}}},
		{"QQ 主人的私聊", handAud{Conv: shadowOwnerPrivate, Owner: true},
			memory.Audience{Channel: "qq", Conversation: shadowOwnerPrivate, Scene: "private", PrivateOK: true, OwnerAsked: true, PersonIDs: []string{owner}}},
		{"QQ 别人的私聊", handAud{Conv: shadowOtherPrivate, Person: shadowStranger},
			memory.Audience{Channel: "qq", Conversation: shadowOtherPrivate, Scene: "private", PersonIDs: []string{shadowStranger}}},
		{"别的 QQ 群", handAud{Conv: shadowOtherGroup},
			memory.Audience{Channel: "qq", Conversation: shadowOtherGroup, Scene: "group", PersonIDs: []string{shadowStranger}}},
		{"别的 QQ 群（主人本人问起）", handAud{Conv: shadowOtherGroup, Owner: true},
			memory.Audience{Channel: "qq", Conversation: shadowOtherGroup, Scene: "group", OwnerAsked: true, PersonIDs: []string{owner}}},
	}
	switch k {
	case kindQQGroup:
		out = append(out,
			auditAud{"来源这个群（群友问）", handAud{Conv: conv}, memory.Audience{Channel: "qq", Conversation: conv, Scene: "group", PersonIDs: []string{shadowStranger}}},
			auditAud{"来源这个群（主人本人问起）", handAud{Conv: conv, Owner: true}, memory.Audience{Channel: "qq", Conversation: conv, Scene: "group", OwnerAsked: true, PersonIDs: []string{owner}}})
	case kindQQOtherPrivate:
		if peer != "" {
			out = append(out, auditAud{"来源这个私聊的对方本人", handAud{Conv: conv, Person: peer}, memory.Audience{Channel: "qq", Conversation: conv, Scene: "private", PersonIDs: []string{peer}}})
		}
	case kindQQOwnerPrivate:
		out = append(out, auditAud{"来源这个私聊（主人本人）", handAud{Conv: conv, Owner: true},
			memory.Audience{Channel: "qq", Conversation: conv, Scene: "private", PrivateOK: true, OwnerAsked: true, PersonIDs: []string{owner}}})
	case kindLocalGroup:
		out = append(out, auditAud{"来源这个本地群聊", handAud{Conv: conv, Owner: true},
			memory.Audience{Channel: "console", Conversation: conv, Scene: "group", PrivateOK: true, PersonIDs: []string{owner}}})
	}
	return out
}

// peerOf：QQ 私聊（某人）里对方的身份，取聊天库里这个会话第一个 peer 的行；认不出是空
func peerOf(ctx context.Context, es memory.EventSource, conv string) (string, error) {
	var after int64
	for scanned := 0; scanned < ownerScanCap; {
		evs, err := es.Events(ctx, conv, after, 200)
		if err != nil {
			return "", err
		}
		for _, e := range evs {
			if e.Origin == "peer" && e.ActorID != "" {
				return e.ActorID, nil
			}
			after = e.Seq
		}
		scanned += len(evs)
		if len(evs) < 200 {
			break
		}
	}
	return "", nil
}

// ownerSpokeBetween：从 from 到 to 之间有没有主人本人的行（片段的范围看这个）
func ownerSpokeBetween(ctx context.Context, es memory.EventSource, conv string, from, to int64) (bool, error) {
	after := from - 1
	for scanned := 0; scanned < ownerScanCap; {
		evs, err := es.Events(ctx, conv, after, 200)
		if err != nil {
			return false, err
		}
		for _, e := range evs {
			if e.Seq > to {
				return false, nil
			}
			if e.ActorKind == "owner" {
				return true, nil
			}
			after = e.Seq
		}
		scanned += len(evs)
		if len(evs) < 200 {
			break
		}
	}
	return false, nil
}

// runSelfCheck 检查 ex 里这次新写的事实和片段
func runSelfCheck(ctx context.Context, ex *extracted, es memory.EventSource) (*checkResult, error) {
	res := &checkResult{}
	srcCache := map[string]struct {
		k    convKind
		peer string
	}{}
	source := func(conv string) (convKind, string, error) {
		if c, ok := srcCache[conv]; ok {
			return c.k, c.peer, nil
		}
		k, err := classifyConv(ctx, es, conv)
		if err != nil {
			return 0, "", err
		}
		peer := ""
		if k == kindQQOtherPrivate {
			if peer, err = peerOf(ctx, es, conv); err != nil {
				return 0, "", err
			}
		}
		srcCache[conv] = struct {
			k    convKind
			peer string
		}{k, peer}
		return k, peer, nil
	}
	judge := func(ref, conv string, scopes, expected []string, k convKind, peer string) {
		for _, s := range scopes {
			if !slices.Contains(expected, s) {
				res.Wider++
				res.Findings = append(res.Findings, finding{Ref: ref, Audience: "范围里的 " + maskNumbers(s), Kind: "wider"})
			}
		}
		for _, a := range auditAudiences(conv, k, peer) {
			res.Cells++
			actual := memory.CanSee(scopes, memory.DisclosureScopes(a.Real))
			want := a.Hand.seesAny(expected)
			switch {
			case actual && !want:
				res.Overreach++
				res.Findings = append(res.Findings, finding{Ref: ref, Audience: a.Name, Kind: "overreach", Actual: true})
			case !actual && want:
				res.Missed++
				res.Findings = append(res.Findings, finding{Ref: ref, Audience: a.Name, Kind: "missed"})
			}
		}
	}
	// 这次写的事实，和这次提升成有效的导入旧候选（提升以后它们才会被召回、给模型看到，所以范围也要过一遍）
	for _, f := range ex.auditFacts() {
		res.Facts++
		k, peer, err := source(f.Conversation)
		if err != nil {
			return nil, err
		}
		if k == kindUnknown {
			res.Unjudged = append(res.Unjudged, f.Ref)
			continue
		}
		exp := expectedFactScopes(k, f.Conversation, peer, colOf(f.Subject))
		if k == kindQQOtherPrivate && colOf(f.Subject) == colOwnerOf && slices.Contains(f.Scopes, "owner") {
			res.OwnerFromPeer++
		}
		judge(f.Ref, f.Conversation, f.Scopes, exp, k, peer)
	}
	for _, e := range ex.newEpisodes() {
		res.Episodes++
		k, peer, err := source(e.Conv)
		if err != nil {
			return nil, err
		}
		if k == kindUnknown {
			res.Unjudged = append(res.Unjudged, e.Ref)
			continue
		}
		spoke := false
		if k == kindQQGroup {
			if spoke, err = ownerSpokeBetween(ctx, es, e.Conv, e.From, e.To); err != nil {
				return nil, err
			}
		}
		judge(e.Ref, e.Conv, e.Scopes, expectedEpisodeScopes(k, e.Conv, peer, spoke), k, peer)
	}
	res.ProtectedChanged = append(res.ProtectedChanged, ex.ProtectedChanged...)
	sort.SliceStable(res.Findings, func(i, j int) bool { return res.Findings[i].Ref < res.Findings[j].Ref })
	return res, nil
}

// summary 是写进 summary.json 的部分：只有计数、编号和听众名
func (c *checkResult) summary() map[string]any {
	type row struct {
		Ref      string `json:"ref"`
		Audience string `json:"audience"`
		Kind     string `json:"kind"`
	}
	var rows []row
	for _, f := range c.Findings {
		rows = append(rows, row{f.Ref, f.Audience, f.Kind})
	}
	return map[string]any{
		"facts": c.Facts, "episodes": c.Episodes, "cells": c.Cells,
		"overreach": c.Overreach, "missed": c.Missed, "wider": c.Wider,
		"unjudged": c.Unjudged, "owner_from_peer_private": c.OwnerFromPeer, "findings": rows,
		"protected_changed": c.ProtectedChanged,
	}
}

func selfCheckMD(r *runResult) string {
	c := r.Check
	if c == nil {
		return "自检没跑（这次没有写出东西，或者读副本失败了）。"
	}
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	p("预期来自 expect.go 里手写的表（规格-记忆-v2 4.2 和第八节），不调用被测的 CanSee；实算是库里存下的真实范围过 CanSee(AllowedScopes)。来源场合取聊天库里的真实记录认的，不用整理时推断的。")
	p("")
	p("- 检查了 %d 条事实（这次新写的，加上这次提升成有效的导入旧候选）、%d 个片段，共 %d 格（条目 × 听众）", c.Facts, c.Episodes, c.Cells)
	p("- **受保护的有效事实被整理动了：%d**（硬条件，必须是 0。跑之前有效、confirmed_by 不是 ai 的事实，跑完状态或 confirmed_by 变了的%s）",
		len(c.ProtectedChanged), yesNo(len(c.ProtectedChanged) == 0, "", "：这些编号 "+strings.Join(c.ProtectedChanged, "、")))
	p("- **越权（实算能看、预期不能看）：%d**（硬条件，必须是 0）", c.Overreach)
	p("- 范围比表宽（范围里有表里没有的值）：%d", c.Wider)
	p("- 漏看（预期能看、实算看不到）：%d（多半是模型选了 private 收紧，只记数）", c.Missed)
	p("- 来源认不出、没法判断的条目：%d%s", len(c.Unjudged), yesNo(len(c.Unjudged) == 0, "", "（"+strings.Join(c.Unjudged, "、")+"）：宁可不放行，请看会话 id 是不是变了形状"))
	p("- 别人在私聊里讲主人的事、范围进了 owner 的：%d 条（Q8，主人定了范围照旧是 owner、要记成待确认的“听说”；第一阶段只整理主人自己的私聊，碰不到）", c.OwnerFromPeer)
	if len(c.Findings) > 0 {
		p("")
		p("发现的明细（编号 · 听众 · 种类）：")
		for _, f := range c.Findings {
			p("- %s · %s · %s", f.Ref, f.Audience, map[string]string{"overreach": "越权", "missed": "漏看", "wider": "范围比表宽"}[f.Kind])
		}
	}
	if ex := r.Extract; ex != nil && len(ex.Duplicates) > 0 {
		text := map[string]string{}
		for _, f := range ex.Facts {
			text[f.Ref] = f.Text
		}
		p("")
		p("同一主体同时有效的相似事实对（%d 组）：", len(ex.Duplicates))
		for _, d := range ex.Duplicates {
			p("- %s「%s」≈ %s「%s」", d[0], shown(text[d[0]]), d[1], shown(text[d[1]]))
		}
	}
	return b.String()
}
