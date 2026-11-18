package main

// 独立预期表（报告 F 节的预期来源，规格-记忆写入上线 W6 报告 F）。
//
// 这一份表有意和 internal/memory 里的 scope.go（AllowedScopes、CanSee、PlaceOf）以及 summary_scope.go（factScopes、episodeScopes）重复：
// 自检是拿"手写的预期"去对"被测代码实算的结果"，预期要是从被测代码里算出来的，两边一起错的时候自检永远是绿的。
// 所以这个文件不调用 memory.CanSee、memory.AllowedScopes、memory.PlaceOf；表的出处是：
//   - 范围：规格-记忆-v2 4.2 的"来源场合 × 主人自己的事 / 她自己的事 / 别人自己的事"那张表；
//   - 谁在哪个场合能想起什么：规格-记忆-v2 第八节"四种场合能想起什么"那张表，加上 ADR-174（群里主人本人问起时多一个 owner）。
// 改这张表等于改"谁能看到什么"的默认值，要先问主人（AGENTS「动手前先读」第 5 条）；不是为了让测试变绿去改它。

import "strings"

// 4.2 那张表的三列
type subjCol int

const (
	colOwnerOf subjCol = iota // 主人自己的事
	colSelfOf                 // 她自己的事
	colOtherOf                // 别人自己的事
)

func colOf(subject string) subjCol {
	switch subject {
	case "owner":
		return colOwnerOf
	case "self":
		return colSelfOf
	}
	return colOtherOf
}

// expectedFactScopes：一条整理写出的事实，按它来源会话的场合（用聊天库里的真实记录认出来的，不是整理时推断的）和所属的列，范围应该是什么。
// peer 是私聊（某人）里对方的身份，认不出对方是谁时为空：按收紧的退路，范围是这个会话本身（"不知道对方是谁：退到这个会话本身"）。
// "平台私聊（某人）里别人说到主人的事"表里是「—」，规格定为 owner、待确认（attributed_to 写说的人）；
// 这是 Q8，主人 2026-11-02 定了范围照现行规则不收紧（ADR-232），这里写成 owner
func expectedFactScopes(k convKind, conv, peer string, col subjCol) []string {
	switch k {
	case kindLocalPrivate, kindLocalGroup, kindQQOwnerPrivate:
		return []string{"owner"}
	case kindQQOtherPrivate:
		if col == colOwnerOf {
			return []string{"owner"}
		}
		if peer != "" {
			return []string{"person:" + peer}
		}
		return []string{"conv:" + conv}
	case kindQQGroup:
		if col == colOwnerOf {
			return []string{"owner", "conv:" + conv}
		}
		return []string{"conv:" + conv}
	}
	return nil
}

// expectedEpisodeScopes：片段是整段对话的摘要，装着这一批里每个人说的话。本地和主人的私聊是 owner；某人的私聊只给那个人；
// 群里是这个群，这一批里主人说过话就再加 owner（话是在群里说的，群里的人都看到了，ADR-159 第 4 条）
func expectedEpisodeScopes(k convKind, conv, peer string, ownerSpoke bool) []string {
	switch k {
	case kindLocalPrivate, kindLocalGroup, kindQQOwnerPrivate:
		return []string{"owner"}
	case kindQQOtherPrivate:
		if peer != "" {
			return []string{"person:" + peer}
		}
		return []string{"conv:" + conv}
	case kindQQGroup:
		if ownerSpoke {
			return []string{"owner", "conv:" + conv}
		}
		return []string{"conv:" + conv}
	}
	return nil
}

// handAud 是一个听众按第八节那张表手写的"能看哪些范围"
type handAud struct {
	All    bool   // 本地私聊里的主人：全部
	Conv   string // 能看 conv:<这个会话>
	Owner  bool   // 能看 owner：主人在场（本地、主人的私聊）或群里主人本人问起
	Person string // 能看 person:<这个人>（私聊对方）；空 = 没有
}

// sees：public 谁都能看；其余按听众的几个字段
func (a handAud) sees(scope string) bool {
	if a.All || scope == "public" {
		return true
	}
	switch {
	case scope == "owner":
		return a.Owner
	case strings.HasPrefix(scope, "conv:"):
		return a.Conv != "" && scope == "conv:"+a.Conv
	case strings.HasPrefix(scope, "person:"):
		return a.Person != "" && scope == "person:"+a.Person
	}
	return false
}

// seesAny：一组范围里任何一个能看就能看（ADR-159 第 4 条）
func (a handAud) seesAny(scopes []string) bool {
	for _, s := range scopes {
		if a.sees(s) {
			return true
		}
	}
	return false
}
