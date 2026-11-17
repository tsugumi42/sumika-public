package config

import "testing"

func TestSelfcheckDefaultsAreValid(t *testing.T) {
	if bad := DefaultSelfcheck().check(); len(bad) != 0 {
		t.Fatalf("默认值应通过校验：%v", bad)
	}
	if !Defaults().Selfcheck.Enabled {
		t.Fatal("运行自检默认开着")
	}
}

func TestSelfcheckBounds(t *testing.T) {
	for name, mut := range map[string]func(*Selfcheck){
		"间隔太短":   func(s *Selfcheck) { s.IntervalSeconds = 29 },
		"间隔太长":   func(s *Selfcheck) { s.IntervalSeconds = 601 },
		"无回执阈值0": func(s *Selfcheck) { s.ReceiptUnknownPerDay = 0 },
		"卡住分钟太短": func(s *Selfcheck) { s.StuckTurnMinutes = 4 },
		"重连次数1":  func(s *Selfcheck) { s.ReconnectCount = 1 },
		"重连窗口0":  func(s *Selfcheck) { s.ReconnectWindowMinutes = 0 },
		"压缩连续1":  func(s *Selfcheck) { s.CompactFailStreak = 1 },
		"阶段连续1":  func(s *Selfcheck) { s.StageFailStreak = 1 },
	} {
		s := DefaultSelfcheck()
		mut(&s)
		if bad := s.check(); len(bad) != 1 {
			t.Errorf("%s：应恰好报一条错误，得到 %v", name, bad)
		}
	}
}
