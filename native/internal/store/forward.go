package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"sumika-native/internal/channel/types"
)

const ForwardJSONMax = 4 << 20

var ErrForwardTooLarge = errors.New("聊天记录超过 4 MiB")

type ForwardRecord struct {
	Tree    types.ForwardTree
	Fetched int64
	Count   int
	Names   []string
}

func (s *Store) ReadForward(ctx context.Context, id string) (ForwardRecord, bool, error) {
	var raw string
	var out ForwardRecord
	err := s.Events.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(tree_json AS BLOB))<=? THEN tree_json ELSE '' END, fetched_at FROM qq_forwards WHERE id=?`, ForwardJSONMax, id).Scan(&raw, &out.Fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if raw == "" {
		return out, false, ErrForwardTooLarge
	}
	if err = json.Unmarshal([]byte(raw), &out.Tree); err != nil {
		return out, false, err
	}
	return out, true, nil
}

// 树和图片、文件任务一起提交，退出后补扫不依赖尚未来得及调用的 OfferForward。
func (s *Store) PutForward(ctx context.Context, tree types.ForwardTree, at int64, conversation string) error {
	if len(tree.Nodes) == 0 {
		return errors.New("不保存空转发层")
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	if len(raw) > ForwardJSONMax {
		return ErrForwardTooLarge
	}
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO qq_forwards(id,tree_json,fetched_at) VALUES(?,?,?)
 ON CONFLICT(id) DO UPDATE SET tree_json=excluded.tree_json,fetched_at=excluded.fetched_at
 WHERE json_array_length(excluded.tree_json,'$.nodes')>json_array_length(qq_forwards.tree_json,'$.nodes')`, tree.ID, string(raw), at)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		for node, n := range tree.Nodes {
			for idx, seg := range n.Segs {
				if seg.Raw == "forward_limit" || (seg.URL == "" && seg.Kind != types.SegFile) || seg.Hash != "" || (seg.Kind != types.SegImage && seg.Kind != types.SegEmoji && seg.Kind != types.SegCard && seg.Kind != types.SegFile) {
					continue
				}
				m := MediaFetch{Idx: idx, Kind: seg.Kind, URLSig: MediaSegSig(seg), State: MediaPending, UpdatedAt: at}
				if err := initForwardMedia(tx, tree.ID, node, conversation, m); err != nil {
					return err
				}
			}
		}
	}
	return s.commitChanged(tx, conversation)
}

type ForwardCandidate struct{ ID, Conversation string }

func (s *Store) ForwardCandidates(ctx context.Context, since int64) ([]ForwardCandidate, error) {
	rows, err := s.Events.QueryContext(ctx, `SELECT DISTINCT json_extract(j.value,'$.ref'), e.conversation FROM events e, json_each(e.segments_json,'$.segs') j
 WHERE e.created_at>=? AND json_valid(e.segments_json) AND json_extract(j.value,'$.kind')='forward'
 AND coalesce(json_extract(j.value,'$.ref'),'')<>'' AND NOT EXISTS(SELECT 1 FROM qq_forwards f WHERE f.id=json_extract(j.value,'$.ref')) LIMIT 50`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForwardCandidate
	for rows.Next() {
		var c ForwardCandidate
		if err = rows.Scan(&c.ID, &c.Conversation); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// 摘要只取前 3 个节点，不把大树传入每条消息。调用方一次查齐当前页的 id。
func (s *Store) ForwardSummaries(ctx context.Context, ids []string) (map[string]ForwardRecord, error) {
	out := map[string]ForwardRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT id,fetched_at,json_array_length(tree_json,'$.nodes'),coalesce(json_extract(tree_json,'$.truncated'),0),
	coalesce(json_extract(tree_json,'$.nodes[0]'),'null'),coalesce(json_extract(tree_json,'$.nodes[1]'),'null'),coalesce(json_extract(tree_json,'$.nodes[2]'),'null'),
	(SELECT json_group_array(substr(coalesce(json_extract(value,'$.name'),''),1,80)) FROM json_each(tree_json,'$.nodes'))
	FROM qq_forwards WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw [3]string
		var r ForwardRecord
		var names string
		if err = rows.Scan(&id, &r.Fetched, &r.Count, &r.Tree.Missing, &raw[0], &raw[1], &raw[2], &names); err != nil {
			return nil, err
		}
		r.Tree.ID = id
		if err = json.Unmarshal([]byte(names), &r.Names); err != nil {
			return nil, err
		}
		for _, body := range raw {
			if body == "null" {
				continue
			}
			var n types.ForwardMessageNode
			if err = json.Unmarshal([]byte(body), &n); err != nil {
				return nil, err
			}
			r.Tree.Nodes = append(r.Tree.Nodes, n)
		}
		out[id] = r
	}
	return out, rows.Err()
}
