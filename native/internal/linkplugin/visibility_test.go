package linkplugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 四种场合共用相同的观察记录门槛；同名、猜编号、跨群和跨平台不能借到资料。
func TestLinkVisibilityAcrossScenes(t *testing.T) {
	r := newRig(t, &fakeFetcher{}, Options{})
	ctx := context.Background()
	conversations := []string{"console:ca", "console:group:invented", "console:group:other", "qq:private:10001:20001", "qq:group:90001", "qq:group:90002"}
	seqs := make([]int64, len(conversations))
	for i, conv := range conversations {
		st, err := r.set.ForConversation(conv)
		if err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("https://x.com/a/status/%d", 201+i)
		seq, err := st.AppendEvent(store.NewEvent{Channel: strings.Split(conv, ":")[0], Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{"ca"})
		if err != nil {
			t.Fatal(err)
		}
		seqs[i] = seq
		if i < 3 {
			r.p.ObserveLocal(conv, seq, text)
		} else {
			key, err := types.ParseConversation(conv)
			if err != nil {
				t.Fatal(err)
			}
			r.p.OnStored(channel.Stored{Observed: true, Seq: seq, Inbound: types.Inbound{Conv: key, Segs: []types.Seg{{Kind: types.SegText, Text: text}}}})
		}
	}
	r.idle()
	for i, conv := range conversations {
		t.Run(conv, func(t *testing.T) {
			c, err := r.p.context(ctx, turn.ContextIn{CharacterID: "ca", Conversation: conv, State: map[string]any{}})
			if err != nil || c == nil || !strings.Contains(strings.Join(c.Lines, "\n"), fmt.Sprintf("推文正文 %d", 201+i)) {
				t.Fatalf("本会话摘要丢失：%+v %v", c, err)
			}
			for j := range conversations {
				if i != j && strings.Contains(strings.Join(c.Lines, "\n"), fmt.Sprintf("推文正文 %d", 201+j)) {
					t.Fatalf("混入其他场合摘要：%v", c.Lines)
				}
			}
			if c, err := r.p.context(ctx, turn.ContextIn{CharacterID: "cb", Conversation: conv, State: map[string]any{}}); err != nil || c != nil {
				t.Fatalf("未观察的角色取得摘要：%+v %v", c, err)
			}
			for _, character := range []string{"ca", "cb"} {
				res, err := r.p.detail(ctx, turn.ToolCall{CharacterID: character, Conversation: conv, Args: call(fmt.Sprintf(`{"message":%d}`, seqs[i]))})
				if err != nil || res.IsError != (character == "cb") || (character == "ca" && !strings.Contains(res.Text, fmt.Sprintf("推文正文 %d", 201+i))) {
					t.Fatalf("角色 %s 详情门槛不符：%+v %v", character, res, err)
				}
			}
			// 同一平台另一个会话的真实编号，不能因它存在就返回详情。
			other := seqs[[]int{1, 2, 1, 4, 5, 4}[i]]
			res, err := r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: conv, Args: call(fmt.Sprintf(`{"message":%d}`, other))})
			if err != nil || !res.IsError || !strings.Contains(res.Text, "没有这个编号") {
				t.Fatalf("跨会话编号放行：%+v %v", res, err)
			}
			for _, args := range []string{`{}`, `{"message":null}`, `{"message":""}`, `{"message":0}`, `{"message":-1}`, `{"message":1.5}`, `{"message":[]}`, `{"message":{}}`, `{"message":99999}`} {
				res, err := r.p.detail(ctx, turn.ToolCall{CharacterID: "ca", Conversation: conv, Args: call(args)})
				if err != nil || !res.IsError || strings.Contains(res.Text, "推文正文") {
					t.Fatalf("非法编号 %s：%+v %v", args, res, err)
				}
			}
		})
	}
}

// 本地事件没有 QQ 段信封；发言人和超出每条上限的数量仍必须来自真实原文。
func TestLocalLinkSourceAndBudget(t *testing.T) {
	for _, conv := range []string{"console:ca", "console:group:invented"} {
		t.Run(conv, func(t *testing.T) {
			r := newRig(t, &fakeFetcher{text: "文"}, Options{})
			st, err := r.set.ForConversation(conv)
			if err != nil {
				t.Fatal(err)
			}
			text := "https://x.com/a/status/301 https://x.com/a/status/302 https://x.com/a/status/303 https://x.com/a/status/304"
			seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{"ca"})
			if err != nil {
				t.Fatal(err)
			}
			r.p.ObserveLocal(conv, seq, text)
			c, err := r.p.context(context.Background(), turn.ContextIn{CharacterID: "ca", Conversation: conv, State: map[string]any{}})
			if err != nil || c == nil {
				t.Fatalf("缺本地摘要：%+v %v", c, err)
			}
			line := c.Lines[1]
			if !strings.Contains(line, "（所有者 ") || !strings.Contains(line, "另有 1 条") || r.f.calls.Load() != 3 {
				t.Fatalf("本地来源或未处理数量丢失（取数 %d 次）：%s", r.f.calls.Load(), line)
			}
		})
	}
}
