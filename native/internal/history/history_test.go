package history_test

import (
	"strings"
	"testing"

	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 规格第七节 A17：同一毫秒有两份成功的快照时取后生成的那份（id 是 ULID，后生成的大）；失败的不算
func TestLatestEpochSameMillisecond(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bad := "不过"
	for _, e := range []store.Epoch{
		{ID: "01EPOCHB", Summary: "后生成的", Status: "ok"},
		{ID: "01EPOCHA", Summary: "先生成的", Status: "ok"},
		{ID: "01EPOCHC", Summary: "", Status: "failed", Error: &bad},
	} {
		e.Conversation, e.CharacterID, e.FromSeq, e.Kind, e.Trigger, e.Model, e.CreatedAt = "console:a", "a", 1, "chat", "manual", "m", 1000
		if err := st.AddEpoch(e); err != nil {
			t.Fatal(err)
		}
	}
	ep, err := history.LatestEpoch(st.Events, "a", "console:a")
	if err != nil || ep == nil || ep.ID != "01EPOCHB" {
		t.Fatalf("应该取 01EPOCHB：%+v %v", ep, err)
	}
}

// 本地群首次让多个角色共用会话，快照仍属于各自的实例；没有自己的快照不能借别人的。
func TestSnapshotsKeepCharacterAcrossFourScenes(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, conv := range []string{"console:a", "console:group:made-up", "qq:private:20002:30003", "qq:group:40004"} {
		t.Run(conv, func(t *testing.T) {
			for i, id := range []string{"a", "b"} {
				if err := st.AddEpoch(store.Epoch{ID: conv + id, Conversation: conv, CharacterID: id, FromSeq: 1, Summary: "仅属于" + id, Kind: "chat", Trigger: "manual", Model: "fake", Status: "ok", CreatedAt: int64(1000 + i)}); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"a", "b", "c"} {
				snapshot, _, epochID, err := history.For(st.Events, id, conv, 30, nil)
				if err != nil {
					t.Fatal(err)
				}
				if id == "c" {
					if snapshot != nil || epochID != "" {
						t.Fatalf("无快照角色读到别人：%+v %q", snapshot, epochID)
					}
				} else if snapshot == nil || snapshot.Text != history.SummaryHeader+"\n仅属于"+id || epochID != conv+id {
					t.Fatalf("%s 读到别人的快照：%+v %q", id, snapshot, epochID)
				}
			}
		})
	}
}

// 主人 QQ 私聊审批的卡片、结局、被截走的回复行：和试运行一样没说出口，不进模型历史。
func TestRemoteApprovalOriginIsNotSaid(t *testing.T) {
	if !history.NotSaid(history.Event{Origin: history.OriginRemoteApproval}) {
		t.Fatal("审批来源要算没说出口")
	}
	if !strings.Contains(history.NotSaidSQL, "'"+history.OriginRemoteApproval+"'") {
		t.Fatal("SQL 过滤列表里也要有它")
	}
	if history.NotSaid(history.Event{Origin: "owner"}) || history.NotSaid(history.Event{Origin: "peer"}) {
		t.Fatal("别的来源不受影响")
	}
}
