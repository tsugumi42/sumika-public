package store

import (
	"context"
	"database/sql"
	"strings"
)

// DisplayReaction 是贴在一条消息上的一个表情：谁、什么时候、贴了哪个。
// 来源是 kind=notice、notice=reaction、reply_to 指向被贴那条的事件（channel/intake_notice.go 存的）；
// 角色自己的号贴的不入库（Intake.processNotice 跳过），所以这里没有。
// 协议端的"取消回应"目前没有存（snowluma.reaction 把它当别的通知丢掉），所以这里也没有取消：条数只增不减。
type DisplayReaction struct {
	ID          string // 通知事件的 id
	ActorKind   string
	ActorID     string
	DisplayName string // 发通知的人的名字（qq_people 查）；查不到留空，调用方退回号码
	Ref         string // 表情编号，平台给的原样：小黄脸编号，或 Unicode 码位的十进制
	At          int64
}

// 一条消息最多带多少个回应：再多的界面也只能算个数，不必每次整份读出来
const maxReactionsPerMessage = 500

// readNotices 按这页目标的 reply_to 查撤回与回应；通知专用索引避免每页扫描整个会话。
// 引用、撤回、回应仍只认同一个会话、观察范围内的通知。
// 先按目标分组再按时间排，让索引筛目标；每条消息内的回应顺序仍是时间、seq。
// refs 是这页用到的事件 id（本页每一行和它们引用的目标）。
func readNotices(ctx context.Context, tx *sql.Tx, scope DisplayScope, refs map[string]bool) (recalled map[string]bool, reactions map[string][]DisplayReaction, err error) {
	args := []any{scope.Conversation}
	for id := range refs {
		args = append(args, id)
	}
	filter, fa := displayFilter(scope, "e")
	args = append(args, fa...)
	rows, err := tx.QueryContext(ctx, noticesSQL(len(refs), filter), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	recalled = map[string]bool{}
	reactions = map[string][]DisplayReaction{}
	for rows.Next() {
		var target, notice string
		var r DisplayReaction
		if err := rows.Scan(&target, &notice, &r.ID, &r.ActorKind, &r.ActorID, &r.At, &r.Ref); err != nil {
			return nil, nil, err
		}
		switch notice {
		case "recall":
			recalled[target] = true
		case "reaction":
			if r.Ref == "" || len(reactions[target]) >= maxReactionsPerMessage {
				continue // 没有表情编号的认不出是哪个表情
			}
			reactions[target] = append(reactions[target], r)
		}
	}
	return recalled, reactions, rows.Err()
}

// noticesSQL 共用本页和已加载消息的通知查询。
func noticesSQL(refs int, filter string) string {
	return `SELECT e.reply_to,json_extract(e.segments_json,'$.notice'),e.id,e.actor_kind,e.actor_id,e.created_at,COALESCE(json_extract(e.segments_json,'$.ref'),'') FROM events e
		WHERE e.conversation=? AND e.reply_to IN (` + strings.TrimSuffix(strings.Repeat("?,", refs), ",") + `) AND e.kind='notice' AND json_valid(e.segments_json)
		AND json_extract(e.segments_json,'$.notice') IN ('recall','reaction') AND ` + filter + ` ORDER BY e.reply_to,e.created_at,e.seq`
}
