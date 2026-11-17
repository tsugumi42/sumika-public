package turn

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"sumika-native/internal/audience"
)

// 自检会话里存下的每一条事件（主人的话、她的回复）都带 selftest 标记，"她看到了新事件"的通知也带；别的会话一条都不带
func TestSelftestConversationTagsEveryEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		conv func(char string) string
		want bool
	}{
		{"自检话题", func(c string) string { return audience.SelftestConversation(c, audience.SelftestTopicID(c, "")) }, true},
		{"新开的自检话题", func(c string) string { return audience.SelftestConversation(c, audience.SelftestTopicID(c, "abc")) }, true},
		{"普通话题", func(c string) string { return "console:" + c + ":topic:t1" }, false},
		{"主窗口日常", func(c string) string { return "console:" + c }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
			s := h.watchSeen()
			conv := tc.conv(h.c.CharacterID)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if o := <-h.hub.SendLocal(ctx, h.c.CharacterID, conv, "自检的一句", nil); o.Err != nil {
				t.Fatal(o.Err)
			}
			rows, err := h.st.Events.Query(`SELECT actor_kind, COALESCE(segments_json,'') FROM events WHERE conversation=? ORDER BY seq`, conv)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var kind, seg string
				if err := rows.Scan(&kind, &seg); err != nil {
					t.Fatal(err)
				}
				var body struct {
					Tags []string `json:"tags"`
				}
				json.Unmarshal([]byte(seg), &body)
				tagged := len(body.Tags) == 1 && body.Tags[0] == audience.SelftestTag
				if tagged != tc.want {
					t.Fatalf("%s 的事件 segments=%q：带标=%v，要 %v", kind, seg, tagged, tc.want)
				}
				n++
			}
			if n != 2 {
				t.Fatalf("该有主人的话和她的回复两条，实际 %d", n)
			}
			for _, e := range s.waitN(t, 2) {
				if e.Selftest != tc.want {
					t.Fatalf("通知 %+v 的 Selftest 要 %v", e, tc.want)
				}
			}
		})
	}
}
