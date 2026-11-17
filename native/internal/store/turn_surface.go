package store

import (
	"database/sql"
	"errors"
	"strconv"
)

// SurfaceSource 是“这一轮被中断”那行系统行的来源名（events.origin 是 system:turn）。
const SurfaceSource = "turn"

// SurfaceInterrupted 给一个被中断的本地回合留一行系统行，把没挂上任何回复的步骤（已经跑完的工具、被切断的工具）挂在它上面：
// 步骤只在回复落库时才挂到回复上，中断的回合没有回复，不挂就永远读不出来，重启以后这次工具调用直接不见了。
// 系统行不是她说的话，不进她的历史和记忆（和 /goal、定期任务的系统行同一套）。
// 没有要挂的步骤返回 0；同一个回合重复调用只用同一行（segments.turn 认），不会出两行。调用方只传本地会话。
func (s *Store) SurfaceInterrupted(turnID, characterID, conversation string, at int64) (int64, error) {
	var tools, cut int
	if err := s.Trace.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN json_valid(details_json) AND json_extract(details_json, '$.interrupted') = ? THEN 1
		WHEN status = 'error' AND preview = ? THEN 1 ELSE 0 END), 0) FROM steps WHERE turn_id = ? AND kind = 'tool' AND event_seq IS NULL`,
		InterruptedByExit, unknownPreview, turnID).Scan(&tools, &cut); err != nil {
		return 0, err
	}
	if tools == 0 {
		return 0, nil
	}
	var seq int64
	err := s.Events.QueryRow(`SELECT seq FROM events WHERE conversation = ? AND kind = 'notice' AND origin = ? AND json_valid(segments_json)
		AND json_extract(segments_json, '$.turn') = ? ORDER BY seq LIMIT 1`, conversation, "system:"+SurfaceSource, turnID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		text := "程序退出时，这一轮被中断了"
		if cut > 0 {
			text += "：" + strconv.Itoa(cut) + " 个工具没有跑完，结果不知道"
		}
		seq, err = s.AppendEvent(NewEvent{Channel: "console", Conversation: conversation, ActorKind: "system", ActorID: characterID, Origin: "system:" + SurfaceSource,
			Kind: "notice", Text: text, Segments: map[string]any{"notice": "system", "source": SurfaceSource, "turn": turnID}, CreatedAt: at}, []string{characterID})
	}
	if err != nil {
		return 0, err
	}
	return seq, s.AttachSteps(turnID, seq)
}
