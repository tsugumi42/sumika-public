package turn

// P2-9 第 2 步小修：裁剪兜底时聊天里那行的条数只算真正丢掉的，旧摘要覆盖的不算（Dropped - Covered）。

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/store"
)

type noticeTrace struct {
	Rebuild struct {
		Dropped  int  `json:"dropped"`
		Covered  int  `json:"covered"`
		Snapshot bool `json:"snapshot"`
	} `json:"rebuild"`
}

// 验收 7：有旧快照、又裁掉了快照之后的话时，提示里的数字等于 Dropped - Covered
func TestFitHardChatLinesExcludeCovered(t *testing.T) {
	t.Parallel()
	h, _ := newStartHarness(t, harnessOpts{replies: `[` + textReply + `,` + shortSummary + `,` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	var cut int64
	for i := range 20 {
		kind, id := "owner", "owner"
		if i%2 == 1 {
			kind, id = "character", h.c.CharacterID
		}
		seq := h.storeConv(conv, "console", kind, id, strings.Repeat(string([]rune("甲乙丙丁戊己庚辛壬癸")[i%10]), 300))
		if i == 10 {
			cut = seq
		}
	}
	if err := h.st.AddEpoch(store.Epoch{ID: "made-old-summary", Conversation: conv, CharacterID: h.c.CharacterID, FromSeq: cut,
		Summary: "前面的聊天已经整理过了。", Kind: "chat", Trigger: "manual", Status: "ok", CreatedAt: h.hub.now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	h.send("先建实例")
	h.setHard(sizeOf(h.hub.liveOf(h.c.CharacterID)) + 300)
	h.send("过了门槛")
	var cj string
	if err := h.st.Trace.QueryRow(`SELECT coalesce(context_json, '') FROM model_runs WHERE stage = 'reply' ORDER BY rowid DESC LIMIT 1`).Scan(&cj); err != nil {
		t.Fatal(err)
	}
	var gotTrace noticeTrace
	if err := json.Unmarshal([]byte(cj), &gotTrace); err != nil {
		t.Fatal(err)
	}
	tr := gotTrace.Rebuild
	if !tr.Snapshot || tr.Covered <= 0 || tr.Dropped <= tr.Covered {
		t.Fatalf("要造出“有旧快照、又裁掉了快照之后的话”：%+v", tr)
	}
	got := h.compactNotices(conv)
	want := fmt.Sprintf("更早的 %d 条聊天这次放不下了", tr.Dropped-tr.Covered)
	if len(got) != 1 || !strings.Contains(got[0], want) {
		t.Fatalf("提示只该算真正丢掉的 %d 条（Dropped %d、Covered %d）：%v", tr.Dropped-tr.Covered, tr.Dropped, tr.Covered, got)
	}
	if strings.Contains(got[0], fmt.Sprintf("更早的 %d 条", tr.Dropped)) {
		t.Fatalf("不能把摘要覆盖的条数也算进去：%v", got)
	}
}
