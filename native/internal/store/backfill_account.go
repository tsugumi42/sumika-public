package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type accountGapCursor struct {
	Account string
	At, ID  int64
}

func (s *Store) ReadAccountGaps(ctx context.Context, account, cursor string, limit int) (GapPage, error) {
	if account == "" || limit < 1 || limit > 200 {
		return GapPage{}, ErrDisplayParameter
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(account)))
	var c accountGapCursor
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Account != identity || c.ID <= 0 {
			return GapPage{}, ErrDisplayParameter
		}
	}
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GapPage{}, err
	}
	defer tx.Rollback()
	var has int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='channel_gaps'`).Scan(&has); err != nil {
		return GapPage{}, err
	}
	if has == 0 {
		return GapPage{Items: []ChannelGap{}}, tx.Commit()
	}
	columns, err := gapReadColumns(ctx, tx)
	if err != nil {
		return GapPage{}, err
	}
	query := `SELECT ` + columns + ` FROM channel_gaps WHERE channel='qq' AND state IN ('open','blocked','abandoned') AND (conversation LIKE ? OR conversation LIKE 'qq:group:%')`
	args := []any{"qq:private:" + account + ":%"}
	if cursor != "" {
		query += ` AND (from_ms,id)>(?,?)`
		args = append(args, c.At, c.ID)
	}
	query += ` ORDER BY from_ms,id LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return GapPage{}, err
	}
	out := GapPage{Items: []ChannelGap{}}
	for rows.Next() {
		g, e := scanGap(rows)
		if e != nil {
			rows.Close()
			return GapPage{}, e
		}
		out.Items = append(out.Items, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return GapPage{}, err
	}
	out.HasMore = len(out.Items) > limit
	if out.HasMore {
		out.Items = out.Items[:limit]
		last := out.Items[len(out.Items)-1]
		b, _ := json.Marshal(accountGapCursor{identity, last.FromMS, last.ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, tx.Commit()
}
