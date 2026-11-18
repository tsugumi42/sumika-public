package store

import (
	"context"
	"testing"
)

// @ 的名字（AtNames）：先用这一页里同一个号发言时的名字（主人的号在段里的 from.id），没有再查名册；
// 全体成员、不是号的 target 不查；查不到的不放进去（界面统一打码）。
func TestAtNamesFromPageAndRoster(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	if _, err := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES ('90052','名册里的昵称','{}')`); err != nil {
		t.Fatal(err)
	}
	group := "qq:group:90002"
	say := func(kind, id string, seg map[string]any) int64 {
		t.Helper()
		seq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: group, ActorKind: kind, ActorID: id, Kind: "message", Text: "编造正文", Segments: seg, CreatedAt: 100}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	say("owner", "owner", map[string]any{"from": map[string]any{"id": "90040", "card": "主人的群名片"}})
	say("human", "qq:90051", map[string]any{"from": map[string]any{"id": "90051", "nick": "群友甲"}})
	at := func(target string) map[string]any { return map[string]any{"kind": "at", "target": target} }
	seq := say("human", "qq:90051", map[string]any{"from": map[string]any{"id": "90051", "nick": "群友甲"},
		"segs": []any{at("90040"), at("90051"), at("90052"), at("90053"), at("all"), at("不是号"), at("90040")}})
	page, err := qq.ReadDisplay(context.Background(), DisplayScope{Platform: "qq", Conversation: group, Characters: []string{"a"}}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	for _, r := range page.Items {
		if r.Seq == seq {
			got = r.AtNames
		}
		if r.ActorKind == "owner" && r.ActorUID != "90040" {
			t.Errorf("主人的号该从段里的 from.id 取：%q", r.ActorUID)
		}
		if r.ActorKind == "human" && r.ActorUID != "90051" {
			t.Errorf("群友的号该从 actor_id 取：%q", r.ActorUID)
		}
	}
	want := map[string]string{"90040": "主人的群名片", "90051": "群友甲", "90052": "名册里的昵称"}
	if len(got) != len(want) {
		t.Fatalf("AtNames %v，要 %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("@%s 的名字 %q，要 %q（全部：%v）", k, got[k], v, got)
		}
	}
}
