package rpcconv

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcwire"
)

// fill 把每个字段填成非零值（切片两个元素、指针都指向填好的值）。api 新加字段而 rpcconv 漏转时，往返后那个字段变回零值，测试就红。
// 例外：error 字段留空（Result 的错误另测），Result 的 State 固定 ready（只有 ready / stale 才带值上线）。
func fill(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("s" + v.Type().Name())
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(7 + depth))
	case reflect.Uint64:
		v.SetUint(uint64(9 + depth))
	case reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte{1, 2, 255})
			return
		}
		if depth > 3 {
			return
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fill(v.Index(i), depth+1)
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Pointer:
		if depth > 3 {
			return
		}
		p := reflect.New(v.Type().Elem())
		fill(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Interface:
		if v.Type() == reflect.TypeFor[error]() {
			return
		}
		v.Set(reflect.ValueOf("any"))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanSet() {
				continue
			}
			fill(f, depth)
		}
		if st := v.FieldByName("State"); st.IsValid() && st.Type() == reflect.TypeFor[api.State]() {
			st.SetString(string(api.Ready))
		}
	}
}

func filled[T any]() T {
	var v T
	fill(reflect.ValueOf(&v).Elem(), 0)
	return v
}

// roundTrip：api → 线上 → JSON → 线上 → api
func roundTrip[A, W any](t *testing.T, in A, out func(A) W, back func(W) A) A {
	t.Helper()
	b, err := json.Marshal(out(in))
	if err != nil {
		t.Fatal(err)
	}
	var w W
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return back(w)
}

func TestRoundTripKeepsEveryField(t *testing.T) {
	check := func(name string, want, got any) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s 往返后不一样（rpcconv 漏转了字段？）\n要 %#v\n得 %#v", name, want, got)
		}
	}
	lp := filled[ctlproto.LogPage]()
	check("LogPage", lp, roundTrip(t, lp, LogPageOut, LogPageIn))
	ld := filled[ctlproto.LogDetail]()
	check("LogDetail", ld, roundTrip(t, ld, LogDetailOut, LogDetailIn))
	wr := filled[api.WhisperReceipt]()
	check("WhisperReceipt", wr, roundTrip(t, wr, WhisperReceiptOut, WhisperReceiptIn))
	m := filled[api.MessageView]()
	check("MessageView", m, roundTrip(t, m, MessageOut, MessageIn))
	p := filled[api.MessagePage]()
	check("MessagePage", p, roundTrip(t, p, PageOut, PageIn))
	o := filled[api.Overview]()
	check("Overview", o, roundTrip(t, o, OverviewOut, OverviewIn))
	s := filled[api.ConversationSnapshot]()
	check("ConversationSnapshot", s, roundTrip(t, s, SnapshotOut, SnapshotIn))
	st := filled[api.Settings]()
	// any 字段经 JSON 回来还是字符串，map 也一样，所以可以直接比
	check("Settings", st, roundTrip(t, st, SettingsOut, SettingsIn))
	md := filled[api.MediaView]()
	check("MediaView", md, roundTrip(t, md, MediaOut, MediaIn))
	r := filled[api.ConversationRequest]()
	check("ConversationRequest", r, roundTrip(t, r, ConversationRequestOut, ConversationRequestIn))
	or := filled[api.OverviewRequest]()
	check("OverviewRequest", or, roundTrip(t, or, OverviewRequestOut, OverviewRequestIn))
	ref := filled[api.MediaRef]()
	check("MediaRef", ref, roundTrip(t, ref, MediaRefOut, MediaRefIn))
	fl := filled[api.List[api.FaceView]]()
	check("Faces", fl, roundTrip(t, fl, FacesOut, FacesIn))
	rt := filled[api.RuntimeView]()
	check("RuntimeView", rt, roundTrip(t, rt, RuntimeOut, RuntimeIn))

	// 第 2 批
	ap := filled[api.LocalApproval]()
	check("LocalApproval", ap, roundTrip(t, ap, ApprovalOut, ApprovalIn))
	apl := filled[[]api.LocalApproval]()
	check("LocalApprovals", apl, roundTrip(t, apl, ApprovalsOut, ApprovalsIn))
	rule := filled[api.ApprovalRule]()
	check("ApprovalRule", rule, roundTrip(t, rule, RuleOut, RuleIn))
	rules := filled[[]api.ApprovalRule]()
	check("ApprovalRules", rules, roundTrip(t, rules, RulesOut, RulesIn))
	am := filled[api.ConversationApprovalMode]()
	check("ConversationApprovalMode", am, roundTrip(t, am, ApprovalModeOut, ApprovalModeIn))
	dr := filled[api.LocalDraft]()
	check("LocalDraft", dr, roundTrip(t, dr, DraftOut, DraftIn))
	parts := filled[[]api.LocalPart]()
	check("LocalParts", parts, roundTrip(t, parts, PartsOut, PartsIn))
	sub := filled[api.LocalInputSubmission]()
	check("LocalInputSubmission", sub, roundTrip(t, sub, SubmissionOut, SubmissionIn))
	att := filled[api.LocalAttachment]()
	check("LocalAttachment", att, roundTrip(t, att, AttachmentOut, AttachmentIn))
	rc := filled[api.InputReceipt]()
	check("InputReceipt", rc, roundTrip(t, rc, ReceiptOut, ReceiptIn))
	sr := filled[api.StopReceipt]()
	check("StopReceipt", sr, roundTrip(t, sr, StopReceiptOut, StopReceiptIn))
	run := filled[api.LocalRunSnapshot]()
	check("LocalRunSnapshot", run, roundTrip(t, run, RunOut, RunIn))
	held := filled[[]api.HeldInput]()
	check("HeldInput", held, roundTrip(t, held, HeldOut, HeldIn))
	qs := filled[[]api.LocalQuestion]()
	check("LocalQuestions", qs, roundTrip(t, qs, QuestionsOut, QuestionsIn))
	ws := filled[api.WorkspaceInfo]()
	check("WorkspaceInfo", ws, roundTrip(t, ws, WorkspaceOut, WorkspaceIn))
	cl := filled[[]api.CheckpointSummary]()
	check("CheckpointSummary", cl, roundTrip(t, cl, CheckpointListOut, CheckpointListIn))
	cp := filled[api.CheckpointPlan]()
	check("CheckpointPlan", cp, roundTrip(t, cp, CheckpointPlanOut, CheckpointPlanIn))
	cr := filled[api.CheckpointRestore]()
	check("CheckpointRestore", cr, roundTrip(t, cr, CheckpointRestoreOut, CheckpointRestoreIn))
	cg := filled[api.CheckpointProgress]()
	check("CheckpointProgress", cg, roundTrip(t, cg, CheckpointProgressOut, CheckpointProgressIn))
	// 目标、定期任务、提醒
	lst := filled[[]api.LocalStatus]()
	check("LocalStatuses", lst, roundTrip(t, lst, LocalStatusesOut, LocalStatusesIn))
	lsp := filled[api.LoopSpec]()
	check("LoopSpec", lsp, roundTrip(t, lsp, LoopSpecOut, LoopSpecIn))
	rdf := filled[api.ReminderDraft]()
	check("ReminderDraft", rdf, roundTrip(t, rdf, ReminderDraftOut, ReminderDraftIn))
	rpt := filled[api.ReminderPatch]()
	check("ReminderPatch", rpt, roundTrip(t, rpt, ReminderPatchOut, ReminderPatchIn))
	rop := filled[api.ReminderOptions]()
	check("ReminderOptions", rop, roundTrip(t, rop, ReminderOptionsOut, ReminderOptionsIn))
	fr := filled[api.ForwardRequest]()
	check("ForwardRequest", fr, roundTrip(t, fr, ForwardRequestOut, ForwardRequestIn))
	fv := filled[api.ForwardView]()
	check("ForwardView", fv, roundTrip(t, fv, ForwardOut, ForwardIn))
}

func TestResultStates(t *testing.T) {
	out := func(r api.Result[string]) api.Result[string] {
		return roundTrip(t, r, func(r api.Result[string]) rpcwire.Result[string] { return ResultOut(r, Same[string]) },
			func(w rpcwire.Result[string]) api.Result[string] { return ResultIn(w, Same[string]) })
	}
	// 失败：错误文字带过去，值不带
	got := out(api.Result[string]{State: api.Failed, Value: "旧", Err: errors.New("查不到")})
	if got.State != api.Failed || got.Value != "" || got.Err == nil || got.Err.Error() != "查不到" {
		t.Errorf("失败的结果往返不对：%#v", got)
	}
	// 刷新失败但带旧值
	got = out(api.Result[string]{State: api.Failed, Value: "旧", Err: errors.New("超时"), Stale: true})
	if !got.Stale || got.Value != "旧" || got.Err == nil {
		t.Errorf("带旧值的失败往返不对：%#v", got)
	}
	got = out(api.Result[string]{State: api.Unsupported, Err: errors.New("没有")})
	if got.State != api.Unsupported {
		t.Errorf("不支持往返不对：%#v", got)
	}
	// 没有引用（State 为空）不上线，回来仍是零值
	m := api.MessageView{Text: "x"}
	w := MessageOut(m)
	if w.Reply != nil {
		t.Errorf("没有引用的消息也发了 reply：%#v", w.Reply)
	}
	b, _ := json.Marshal(w)
	if string(b) != `{"ref":{"conversation":{},"event":{}},"text":"x"}` {
		t.Errorf("零值字段没省掉：%s", b)
	}
}

func TestForwardLinesTrimTrailingBlank(t *testing.T) {
	s := api.DisplaySegment{Kind: "forward", Forward: &api.ForwardBrief{Lines: [3]string{"一", "", ""}, Count: 3}}
	w := segmentOut(s)
	if len(w.Forward.Lines) != 1 {
		t.Fatalf("尾部空行应省掉：%#v", w.Forward.Lines)
	}
	if back := segmentIn(w); back.Forward.Lines != s.Forward.Lines {
		t.Fatalf("回来不对：%#v", back.Forward.Lines)
	}
}

// 改提醒：指针非空才算改。空数组、false 也是「改成这个」，不能在线上丢掉
func TestReminderPatchKeepsWhichFieldsAreSet(t *testing.T) {
	none, no := []string{}, false
	got := roundTrip(t, api.ReminderPatch{Targets: &none, Paused: &no}, ReminderPatchOut, ReminderPatchIn)
	if got.Text != nil || got.At != nil || got.Repeat != nil {
		t.Fatalf("没改的项不该冒出来：%#v", got)
	}
	if got.Targets == nil || len(*got.Targets) != 0 || got.Paused == nil || *got.Paused {
		t.Fatalf("改成空数组、false 丢了：%#v", got)
	}
	if empty := roundTrip(t, api.ReminderPatch{}, ReminderPatchOut, ReminderPatchIn); empty.Text != nil || empty.Targets != nil || empty.Paused != nil || empty.Repeat != nil || empty.At != nil {
		t.Fatalf("空补丁往返后不该有任何一项：%#v", empty)
	}
}
