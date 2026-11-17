package store

import "database/sql"

// DisplayStep 是挂在一条回复上的一步（思考或一次工具调用），只给本地主窗口看。
type DisplayStep struct {
	ID, Kind, Name, Status, Preview string
	Args                            string
	IsError                         bool
	StartedAt, EndedAt              int64
	Interrupted                     string // details_json.interrupted：stop / exit；空是正常结束的
	Approval                        string // approval_json 原文：这一步怎么放行的
}

// ReadSteps 读这条回复之前那段思考和工具（回复落盘时 AttachSteps 挂上的），按开始的先后。
func (s *Store) ReadSteps(conversation string, eventSeq int64) ([]DisplayStep, error) {
	rows, err := s.Trace.Query(`SELECT id, kind, COALESCE(name,''), COALESCE(status,''), COALESCE(preview,''), COALESCE(args_json,''), COALESCE(is_error,0), COALESCE(started_at,0), COALESCE(ended_at,0),
		CASE WHEN json_valid(details_json) THEN COALESCE(json_extract(details_json,'$.interrupted'),'') ELSE '' END, COALESCE(approval_json,'')
		FROM steps WHERE conversation = ? AND event_seq = ? ORDER BY started_at, id`, conversation, eventSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DisplayStep
	for rows.Next() {
		var d DisplayStep
		var isError sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Kind, &d.Name, &d.Status, &d.Preview, &d.Args, &isError, &d.StartedAt, &d.EndedAt, &d.Interrupted, &d.Approval); err != nil {
			return nil, err
		}
		d.IsError = isError.Int64 != 0
		out = append(out, d)
	}
	return out, rows.Err()
}
