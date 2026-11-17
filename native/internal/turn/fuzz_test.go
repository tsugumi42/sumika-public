package turn

// 性质测试：压缩切点。最近一轮原文必须留着，切点必须落在主人（平台上的人）说话那条上，
// 切点之前的才进摘要、之后按原顺序接上；maxSeq 限制只能压记忆整理过的部分。

import (
	"strings"
	"testing"

	"sumika-native/internal/history"
)

// fuzzEvents 把一行一条的文本变成事件：o 主人、h 平台上的人、c 她、n 通知（不算消息）、别的当普通消息。
// seq 从 1 开始，和库里一样递增。
func fuzzEvents(raw string) []history.Event {
	var out []history.Event
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		kind, actor, text := "message", "character", line
		tag, rest, _ := strings.Cut(line, " ")
		switch {
		case strings.HasPrefix(tag, "o"):
			actor, text = "owner", rest
		case strings.HasPrefix(tag, "h"):
			actor, text = "human", rest
		case strings.HasPrefix(tag, "c"):
			actor, text = "character", rest
		case strings.HasPrefix(tag, "n"):
			kind, actor, text = "notice", "owner", rest
		case strings.HasPrefix(tag, "x"):
			kind, actor, text = "context", "owner", rest
		}
		out = append(out, history.Event{Seq: int64(len(out) + 1), Kind: kind, ActorKind: actor, Text: text})
	}
	return out
}

// FuzzCutPoint 覆盖 cutPoint 的边界：没有切点是允许的；有切点时它必须是主人/人的消息 seq、
// 不晚于 maxSeq、不越过最近一轮，切点之后的消息按原顺序各出现一次。
func FuzzCutPoint(f *testing.F) {
	seeds := [][]any{
		{"", -1, int64(0)},
		{"o 早\nc 早呀\no 吃了吗\nc 吃了", 1, int64(0)},
		{"o 一\nc 二\no 三\nc 四\no 五\nc 六", 120, int64(0)},
		{"o 一\nc 二\nn 通知\no 三\nc 四\no 五\nc 六", 20, int64(4)},
		{"h 平台上的人\no 主人\nc 回复\nh 又问\nc 又答", 5, int64(0)},
		{"o " + strings.Repeat("长", 500) + "\nc 短\no 再问\nc 再答", 0, int64(0)},
		{"o 一\nc 二\no 三\nc 四", 1, int64(2)},
	}
	for _, s := range seeds {
		f.Add(s[0].(string), s[1].(int), s[2].(int64))
	}
	f.Fuzz(func(t *testing.T, raw string, keep int, maxSeq int64) {
		events := fuzzEvents(raw)
		cut := cutPoint(events, keep, maxSeq)
		if cut == 0 {
			return
		}
		var msgs []history.Event
		lastAsk := -1
		for _, e := range events {
			if e.Kind != "message" {
				continue
			}
			if e.ActorKind == "owner" || e.ActorKind == "human" {
				lastAsk = len(msgs)
			}
			msgs = append(msgs, e)
		}
		idx := -1
		for i, e := range msgs {
			if e.Seq == cut {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("切点 %d 不是消息的 seq：%+v", cut, events)
		}
		if a := msgs[idx].ActorKind; a != "owner" && a != "human" {
			t.Fatalf("切点 %d 落在 %q 的消息上，应该落在主人/人说话那条：%+v", cut, a, events)
		}
		if idx > lastAsk {
			t.Fatalf("切点 %d 越过了最近一轮（最近主人第 %d 条）：%+v", cut, lastAsk, events)
		}
		if maxSeq > 0 && cut > maxSeq {
			t.Fatalf("切点 %d 晚于 maxSeq %d：%+v", cut, maxSeq, events)
		}
		var prev int64
		for _, e := range msgs[idx:] {
			if e.Seq <= prev {
				t.Fatalf("切点之后顺序不对或有重复：切点 %d，消息 %+v", cut, msgs[idx:])
			}
			prev = e.Seq
		}
	})
}
