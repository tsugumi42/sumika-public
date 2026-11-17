package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// LinkSource 复核不可伪造的入站身份、标签和撤回；不补 observations。
func (s *Store) LinkSource(ctx context.Context, conversation string, seq int64) (EventRef, bool, error) {
	var ref EventRef
	var raw sql.NullString
	e := s.Events.QueryRowContext(ctx, `SELECT e.seq,e.id,e.actor_kind,e.actor_id,COALESCE(e.origin,''),e.segments_json FROM events e
 WHERE e.seq=? AND e.conversation=? AND e.channel='qq' AND e.kind='message'
 AND NOT EXISTS(SELECT 1 FROM events r WHERE r.conversation=e.conversation AND r.seq>e.seq AND r.kind='notice' AND r.reply_to=e.id AND json_valid(r.segments_json) AND json_extract(r.segments_json,'$.notice')='recall')`, seq, conversation).Scan(&ref.Seq, &ref.ID, &ref.ActorKind, &ref.ActorID, &ref.Origin, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return ref, false, nil
	}
	if e != nil {
		return ref, false, e
	}
	if ref.ActorKind != "human" && ref.ActorKind != "owner" {
		return ref, false, nil
	}
	if ref.Origin != "peer" && ref.Origin != "owner" {
		return ref, false, nil
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &body) != nil {
		return ref, false, nil
	}
	for _, tag := range []string{"backfill", "fromSelf", "stranger", "selftest"} {
		if slices.Contains(body.Tags, tag) {
			return ref, false, nil
		}
	}
	return ref, true, nil
}
