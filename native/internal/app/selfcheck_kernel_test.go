package app

import (
	"testing"
	"time"

	"sumika-native/internal/turn"
)

// 回合内核的故障原样交给自检：会话、种类、时间、原因、重试次数一项不丢；内核故障（会话为空）标成 Kernel。
func TestKernelFaultsOfKeepsEveryField(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 14, 22, 0, 0, 0, time.Local)
	got := kernelFaultsOf([]turn.Fault{
		{Scope: turn.FaultScopeKernel, Kind: turn.FaultStartupRecover, At: at, Detail: "made-1"},
		{Scope: turn.FaultScopeConversation, Kind: turn.FaultRunState, CharacterID: "c1", Conversation: "console:c1", At: at.Add(time.Minute), Detail: "made-2", Tries: 3},
	})
	if len(got) != 2 {
		t.Fatalf("两条故障：%+v", got)
	}
	if !got[0].Kernel || got[0].Kind != "startup_recover" || got[0].Detail != "made-1" || !got[0].At.Equal(at) {
		t.Errorf("内核故障：%+v", got[0])
	}
	if got[1].Kernel || got[1].Conversation != "console:c1" || got[1].CharacterID != "c1" || got[1].Kind != "run_state" || got[1].Tries != 3 || got[1].Detail != "made-2" {
		t.Errorf("会话故障：%+v", got[1])
	}
	if n := len(kernelFaultsOf(nil)); n != 0 {
		t.Errorf("没有故障就是空：%d", n)
	}
}
