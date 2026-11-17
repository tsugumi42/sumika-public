package store

import (
	"context"
	"errors"
	"testing"

	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
)

func TestInputAdmissionCrashWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := set.For("console")
	ev := NewEvent{Channel: "console", Conversation: "console:actor-a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "编的任务", CreatedAt: 1}
	seq, err := st.AppendInputEvent(ctx, "stable-input", "digest-a", ev, []string{"actor-a"})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟平台提交完成、trace 尚未提交时退出；重开后按同一个身份补登记。
	if err = set.Close(); err != nil {
		t.Fatal(err)
	}
	set, err = OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	st, _ = set.For("console")
	again, err := st.AppendInputEvent(ctx, "stable-input", "digest-a", ev, []string{"actor-a"})
	if err != nil || again != seq {
		t.Fatalf("事件重放 %d/%d %v", seq, again, err)
	}
	in := KernelInput{Identity: kernel.Invocation{Scope: kernel.Scope{CharacterID: "actor-a", Conversation: ev.Conversation}, InputID: "stable-input", RunID: "run-a", RootID: "run-a", Kind: kernel.KindForeground}, Domain: "actor-a/console", ClientKey: "client-a", Digest: "digest-a", Payload: `{"Text":"编的任务"}`, Mode: kernel.InputQueue}
	first, fresh, err := st.AcceptKernelInput(ctx, in, 32, 256)
	if err != nil || !fresh {
		t.Fatalf("补登记 %+v %v %v", first, fresh, err)
	}
	second, fresh, err := st.AcceptKernelInput(ctx, in, 32, 256)
	if err != nil || fresh || second.Identity != first.Identity || second.Sequence != first.Sequence {
		t.Fatalf("重复受理 %+v %v %v", second, fresh, err)
	}
	in.Digest = "different-body"
	if _, _, err = st.AcceptKernelInput(ctx, in, 32, 256); !errors.Is(err, ErrInputConflict) {
		t.Fatalf("同键换正文未冲突 %v", err)
	}
	if _, err = st.AppendInputEvent(ctx, "stable-input", "different-body", ev, []string{"actor-a"}); !errors.Is(err, ErrInputConflict) {
		t.Fatalf("聊天事件同键换正文 %v", err)
	}
	var events, inputs int
	st.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	st.Trace.QueryRow(`SELECT count(*) FROM kernel_inbox`).Scan(&inputs)
	if events != 1 || inputs != 1 {
		t.Fatalf("重复落库 events=%d inbox=%d", events, inputs)
	}
	t.Logf("崩溃窗口恢复：events=%d inbox=%d seq=%d sequence=%d", events, inputs, seq, first.Sequence)
}

func TestInboxRestartHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	st := set.TraceStore()
	for _, id := range []string{"pending", "admitted", "done"} {
		row, _, err := st.AcceptKernelInput(ctx, KernelInput{Identity: kernel.Invocation{InputID: id, RunID: id, RootID: id, Kind: kernel.KindForeground}, Domain: "scope-a", ClientKey: id, Digest: id, Payload: `{}`, Mode: kernel.InputQueue}, 32, 256)
		if err != nil {
			t.Fatal(err)
		}
		if id != "pending" {
			if err = st.AdmitKernelInput(ctx, row.Identity.InputID); err != nil {
				t.Fatal(err)
			}
		}
		if err = st.TurnStart(id, "actor-a", "console:actor-a", "console", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.KernelRunState(ctx, "done", kernel.RunSettled, kernel.OutcomeDone, true); err != nil {
		t.Fatal(err)
	}
	if err = st.TurnEnd("done", "done", 2); err != nil {
		t.Fatal(err)
	}
	for _, step := range []Step{{ID: "waiting", TurnID: "admitted", Kind: "tool", Status: "waiting", CharacterID: "actor-a", Conversation: "console:actor-a"}, {ID: "sent", TurnID: "admitted", Kind: "tool", Status: "running", CharacterID: "actor-a", Conversation: "console:actor-a"}} {
		if err = st.StepStart(step); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.ActivityStart(ctx, "transport", kernel.Invocation{RunID: "admitted"}); err != nil {
		t.Fatal(err)
	}
	if err = set.Close(); err != nil {
		t.Fatal(err)
	}
	set, err = OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	st = set.TraceStore()
	if err = st.RecoverKernelInputs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SweepInterrupted(3); err != nil {
		t.Fatal(err)
	}
	assert := func(q, want string) {
		t.Helper()
		var got string
		if err := st.Trace.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %q want=%q err=%v", q, got, want, err)
		}
	}
	assert(`SELECT state FROM kernel_inbox WHERE input_id='pending'`, "held")
	assert(`SELECT outcome FROM kernel_runs WHERE run_id='admitted'`, "interrupted")
	assert(`SELECT outcome FROM kernel_runs WHERE run_id='done'`, "done")
	assert(`SELECT status FROM steps WHERE id='waiting'`, "cancelled")
	assert(`SELECT status FROM steps WHERE id='sent'`, "error")
	assert(`SELECT state FROM kernel_activities WHERE id='transport'`, "unknown")
	assert(`SELECT preview FROM steps WHERE id='sent'`, unknownPreview)
	t.Log("重开真实临时 SQLite：pending=held admitted=interrupted waiting=cancelled dispatched=unknown done=done")
}

func TestKernelScopeCancellationIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	st := set.TraceStore()
	base := kernel.Scope{CharacterID: "actor-a", Conversation: "console:actor-a", ScopeID: "scope-a", Platform: "local", Channel: "console", Scene: "private", AccountID: "account-a", ActorKind: "human", ActorID: "编的\u2028<&\"身份", AuthorityID: "grant-a", AuthorityRevision: ^uint64(0)}
	for _, tc := range []struct {
		name   string
		change func(*kernel.Scope)
	}{
		{"actor", func(s *kernel.Scope) { s.ActorID += "另一人" }},
		{"revision", func(s *kernel.Scope) { s.AuthorityRevision-- }},
		{"account", func(s *kernel.Scope) { s.AccountID = "account-b" }},
		{"scene", func(s *kernel.Scope) { s.Scene = "group" }},
		{"scope", func(s *kernel.Scope) { s.ScopeID = "scope-b" }},
		{"authority", func(s *kernel.Scope) { s.AuthorityID = "grant-b" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			neighbor := base
			tc.change(&neighbor)
			for i, scope := range []kernel.Scope{base, neighbor} {
				id := tc.name + string(rune('a'+i))
				_, _, err := st.AcceptKernelInput(ctx, KernelInput{Identity: kernel.Invocation{Scope: scope, InputID: id, RunID: id, Kind: kernel.KindForeground}, Domain: tc.name, ClientKey: id, Digest: id, Payload: `{}`, Mode: kernel.InputQueue}, 32, 256)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := st.CancelKernelInputs(ctx, tc.name, 2, base); err != nil {
				t.Fatal(err)
			}
			for i, want := range []kernel.InputState{kernel.InputCancelled, kernel.InputAccepted} {
				row, err := st.KernelInput(ctx, tc.name, tc.name+string(rune('a'+i)))
				if err != nil || row.State != want {
					t.Fatalf("跨域取消：%s state=%s want=%s err=%v", tc.name, row.State, want, err)
				}
			}
		})
	}
}

func TestKernelToolIdentity(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	st := set.TraceStore()
	for ordinal, id := range []string{"host-tool-a", "host-tool-b"} {
		in := kernel.Invocation{Scope: kernel.Scope{CharacterID: "actor-a", Conversation: "console:actor-a"}, RunID: "run-a", RootID: "run-a", ToolID: id, StepID: id, ProviderCallID: "provider-reused-id", Kind: kernel.KindForeground}
		if err := st.StepStart(Step{ID: id, CharacterID: in.Scope.CharacterID, Conversation: in.Scope.Conversation, TurnID: in.RunID, Kind: "tool", Name: "read", StartedAt: int64(ordinal + 1), Identity: in, Ordinal: &ordinal}); err != nil {
			t.Fatal(err)
		}
		var host, provider, step string
		var gotOrdinal int
		if err := st.Trace.QueryRow(`SELECT json_extract(identity_json,'$.ToolID'),provider_call_id,step_id,ordinal FROM steps WHERE id=?`, id).Scan(&host, &provider, &step, &gotOrdinal); err != nil {
			t.Fatal(err)
		}
		if host != id || provider != "provider-reused-id" || step != in.StepID || gotOrdinal != ordinal {
			t.Fatalf("调用身份被覆盖 host=%q provider=%q step=%q ordinal=%d", host, provider, step, gotOrdinal)
		}
		in.ProviderCallID = "changed-provider-id"
		if err := st.StepStart(Step{ID: id, CharacterID: in.Scope.CharacterID, Conversation: in.Scope.Conversation, TurnID: in.RunID, Kind: "tool", Name: "read", Identity: in, Ordinal: &ordinal}); !errors.Is(err, ErrInputConflict) {
			t.Fatalf("同宿主工具换绑定未冲突 %v", err)
		}
	}
}
