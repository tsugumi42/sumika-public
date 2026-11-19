package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"sumika-native/internal/channel/types"
)

// ImageInput is an observed owner's event; only its own image segments may be sent.
type ImageInput struct {
	Seq              int64
	Text, Line, Seen string
	Segs             []types.Seg  // QQ 来的消息按段存
	Local            []LocalImage // 本地私聊主人消息里各块的图片附件（local_parts）
}

// LocalImage 是本地消息块上的一个图片附件：媒体库里的图（Hash，Conversation 是来源会话，空就是本机上传）或主人给的本地文件路径。
type LocalImage struct {
	Hash, Path, Conversation string
}

func (s *Store) ReadImageInput(ctx context.Context, character, conversation string, seq int64) (ImageInput, bool, error) {
	var out ImageInput
	var raw string
	err := s.Events.QueryRowContext(ctx, `SELECT e.seq,e.text,coalesce(e.segments_json,'{}') FROM events e
		WHERE e.seq=? AND e.conversation=? AND e.kind='message' AND e.actor_kind='owner'
		AND EXISTS(SELECT 1 FROM observations o WHERE o.event_seq=e.seq AND o.character_id=?)`, seq, conversation, character).Scan(&out.Seq, &out.Text, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	var body struct {
		Segs  []types.Seg `json:"segs"`
		Line  string      `json:"line"`
		Seen  string      `json:"seen"`
		Parts []struct {
			Attachments []struct {
				Kind         string `json:"kind"`
				Hash         string `json:"hash"`
				Path         string `json:"path"`
				Conversation string `json:"conversation"`
			} `json:"attachments"`
		} `json:"local_parts"`
	}
	if err = json.Unmarshal([]byte(raw), &body); err != nil {
		return out, false, err
	}
	out.Segs, out.Line, out.Seen = body.Segs, body.Line, body.Seen
	for _, p := range body.Parts {
		for _, a := range p.Attachments {
			if a.Kind == "image" {
				out.Local = append(out.Local, LocalImage{Hash: a.Hash, Path: a.Path, Conversation: a.Conversation})
			}
		}
	}
	return out, true, nil
}
