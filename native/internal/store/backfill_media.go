package store

import (
	"context"
	"encoding/json"
	"sumika-native/internal/channel/types"
)

// AlignmentMediaTarget 是轮次内的段身份，不带 URL、文件名或正文。
type AlignmentMediaTarget struct {
	Seq                                int64
	Index, Node                        int
	Forward, Conversation, Kind, State string
	Hash                               string
}

// AlignmentMediaTargets 读连接窗口及本轮新增事件；媒体段按身份去重，不按消息数计数。
func (s *Store) AlignmentMediaTargets(ctx context.Context, account string, since, newAfter, through int64, allowed func(string) bool) ([]AlignmentMediaTarget, error) {
	type forwardRef struct{ conv, id string }
	var roots []forwardRef
	rootIDs := map[string]bool{}
	// 正文媒体与转发入口共用一次窗口扫描，避免重复展开同一批事件 JSON。
	rows, err := s.Events.QueryContext(ctx, `SELECT e.seq,e.conversation,CAST(j.key AS INTEGER),json_extract(j.value,'$.kind'),coalesce(json_extract(j.value,'$.hash'),''),coalesce(m.state,''),coalesce(json_extract(j.value,'$.ref'),'')
 FROM events e,json_each(CASE WHEN json_valid(e.segments_json) THEN e.segments_json ELSE '{}' END,'$.segs') j
 LEFT JOIN media_fetch m ON m.event_seq=e.seq AND m.idx=CAST(j.key AS INTEGER)
 WHERE e.channel='qq' AND e.seq<=? AND (e.conversation LIKE ? OR e.conversation LIKE 'qq:group:%')
 AND (e.created_at>=? OR e.seq>?)
 AND ((e.created_at>=? AND json_extract(j.value,'$.kind') IN ('image','emoji','card','file','forward'))
 OR (e.seq>? AND json_extract(j.value,'$.kind') IN ('image','emoji','card','video','file','forward')))
 AND CASE WHEN json_extract(j.value,'$.kind')='forward' THEN coalesce(json_extract(j.value,'$.ref'),'')<>''
 ELSE coalesce(json_extract(j.value,'$.url'),'')<>'' OR coalesce(json_extract(j.value,'$.hash'),'')<>'' OR json_extract(j.value,'$.kind')='file' END`, through, "qq:private:"+account+":%", since, newAfter, since, newAfter)
	if err != nil {
		return nil, err
	}
	var out []AlignmentMediaTarget
	for rows.Next() {
		var t AlignmentMediaTarget
		var ref string
		if err = rows.Scan(&t.Seq, &t.Conversation, &t.Index, &t.Kind, &t.Hash, &t.State, &ref); err != nil {
			rows.Close()
			return nil, err
		}
		if !allowed(t.Conversation) {
			continue
		}
		if t.Kind == "forward" {
			if !rootIDs[ref] {
				rootIDs[ref] = true
				roots = append(roots, forwardRef{t.Conversation, ref})
			}
		} else {
			out = append(out, t)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// 仅遍历现有可见树；没有树的资源单列，不能把未取树当作下载完成。
	seen := map[string]bool{}
	var visit func(forwardRef, int, *int) error
	visit = func(r forwardRef, depth int, budget *int) error {
		if seen[r.id] || depth > types.ForwardDepth || *budget == 0 {
			return nil
		}
		seen[r.id] = true
		row, ok, err := s.ReadForward(ctx, r.id)
		if err != nil {
			return err
		}
		if !ok {
			out = append(out, AlignmentMediaTarget{Forward: r.id, Conversation: r.conv, Kind: "forward", State: "unchecked"})
			return nil
		}
		states, err := s.ForwardMediaRows(ctx, r.id)
		if err != nil {
			return err
		}
		nodes := row.Tree.Nodes[:min(len(row.Tree.Nodes), *budget)]
		*budget -= len(nodes)
		for node, n := range nodes {
			for idx, seg := range n.Segs {
				if seg.Kind == "forward" && seg.Ref != "" {
					if err = visit(forwardRef{r.conv, seg.Ref}, depth+1, budget); err != nil {
						return err
					}
					continue
				}
				if seg.Kind != "image" && seg.Kind != "emoji" && seg.Kind != "card" && seg.Kind != "video" && seg.Kind != "file" {
					continue
				}
				if seg.URL == "" && seg.Hash == "" && seg.Kind != "file" {
					continue
				}
				state := states[[2]int{node, idx}].State
				if seg.Raw == "forward_limit" {
					state = "skipped"
				}
				out = append(out, AlignmentMediaTarget{Forward: r.id, Node: node, Index: idx, Conversation: r.conv, Kind: seg.Kind, Hash: seg.Hash, State: state})
			}
		}
		return nil
	}
	for _, r := range roots {
		budget := types.ForwardNodes
		if err = visit(r, 1, &budget); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) BackfillEventHighWater(ctx context.Context) (int64, error) {
	var seq int64
	err := s.Events.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM events`).Scan(&seq)
	return seq, err
}
func (s *Store) UpdateAlignmentMedia(ctx context.Context, account, id string, media any) error {
	b, err := json.Marshal(media)
	if err != nil {
		return err
	}
	_, err = s.Events.ExecContext(ctx, `UPDATE channel_alignment_latest SET media_json=? WHERE account=? AND job_id=?`, string(b), account, id)
	if err == nil {
		s.changed("@alignment")
	}
	return err
}
