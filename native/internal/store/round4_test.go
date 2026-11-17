package store

import "testing"

// 相同判断的重复回填不能覆盖首个结局，也不能再执行一次 UPDATE。
func TestGateOutcomeFirstWriteWins(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	id, err := s.AddGateDecision(GateDecisionRow{CharacterID: "example", Conversation: "qq:group:300", Verdict: "forced", Reason: "编的原因"})
	mustOK(t, err)
	_, err = s.Trace.Exec(`CREATE TABLE outcome_writes (id INTEGER); CREATE TRIGGER count_outcomes AFTER UPDATE OF outcome ON gate_decisions BEGIN INSERT INTO outcome_writes VALUES (NEW.id); END`)
	mustOK(t, err)
	mustOK(t, s.SetGateOutcome(id, GateOutcomeDry, "编的首次结局"))
	mustOK(t, s.SetGateOutcome(id, GateOutcomeError, "编的迟到结局"))
	rows, err := s.GateDecisions("qq:group:300", 10)
	mustOK(t, err)
	var writes int
	mustOK(t, s.Trace.QueryRow(`SELECT count(*) FROM outcome_writes`).Scan(&writes))
	if len(rows) != 1 || rows[0].Outcome != GateOutcomeDry || rows[0].OutcomeReason != "编的首次结局" || writes != 1 {
		t.Fatalf("结局被重复回填：%+v，UPDATE=%d", rows, writes)
	}
}
