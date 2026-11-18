package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 主人问后群友再问：两者都能想起同角色记忆；主人问时保留私事提示，不把它误当召回权限。

func TestExam主人问过后群友再问(t *testing.T) {
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "主人下周要考试，考完才有空", "owner", "owner"},
		{"2", "promise", "active", "考试前提醒他早睡", "owner", "owner"},
		{"3", "fact", "candidate", "主人好像说过在学吉他", "owner", "owner"},
		{"4", "fact", "active", "小A约好下周搬家，群里帮忙", "qq:60001", "conv:" + GA},
		{"5", "fact", "active", "群里都知道她养了一只猫", "qq:60002", "public"},
		{"6", "fact", "active", "主人的猫叫团子，只和她说过", "owner", "owner"},
	}, nil)
	m, err := Open(context.Background(), file, textEmbed{}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	const mark = "所有者私下说的，他问才答："
	ownerRefs := map[string]bool{}
	for _, it := range []string{"1", "2", "3", "6"} {
		ownerRefs["f:"+it] = true
	}

	totalOwner := 0
	// 先后两问，同一个群、同样的字
	for _, q := range []string{"考试", "吉他", "猫", "考试 吉他 搬家 猫"} {
		for _, mode := range []Mode{ModeExplicit, ModeNormal} {
			asked := m.Recall(ctx, qqGroup(GA, true, "owner"), Query{Text: q, Mode: mode, Addressed: true})
			peer := m.Recall(ctx, qqGroup(GA, false, peerA), Query{Text: q, Mode: mode, Addressed: true})

			// 主人问：owner 范围的行都带标记；没有 owner 范围的行不带
			lines := Render(asked, time.Now()).Lines(true)
			nOwner := 0
			for _, h := range append(append([]Hit{}, asked.Pinned...), asked.Hits...) {
				if ownerRefs[h.Ref] {
					nOwner++
					if !h.OwnerOnly {
						t.Errorf("「%s」%v：owner 范围的 %s 没标 OwnerOnly", q, mode, h.Ref)
					}
				} else if h.OwnerOnly {
					t.Errorf("「%s」%v：不是 owner 范围的 %s 被标了 OwnerOnly", q, mode, h.Ref)
				}
			}
			totalOwner += nOwner
			marked := 0
			for _, l := range lines {
				if strings.Contains(l, mark) {
					marked++
				}
			}
			if marked != nOwner {
				t.Errorf("「%s」%v：owner 范围的 %d 条，Render 里带标记的 %d 行（%q）", q, mode, nOwner, marked, lines)
			}

			// 群友仍能想起；请求者变化不带走主人的 OwnerOnly 请求标记。
			for _, h := range append(append(append([]Hit{}, peer.Pinned...), peer.Hits...), peer.Candidates...) {
				if h.OwnerOnly {
					t.Errorf("「%s」%v：群友那一档不该有 OwnerOnly（%s）", q, mode, h.Ref)
				}
			}
			for _, h := range append(append([]Hit{}, asked.Pinned...), asked.Hits...) {
				if ownerRefs[h.Ref] && !hasRef(append(append([]Hit{}, peer.Pinned...), peer.Hits...), h.Ref) {
					t.Errorf("「%s」%v：请求者变化丢掉同角色记忆 %s", q, mode, h.Ref)
				}
			}
			for _, l := range Render(peer, time.Now()).Lines(true) {
				if strings.Contains(l, mark) {
					t.Errorf("「%s」%v：群友那一档的 Render 不该有标记：%q", q, mode, l)
				}
			}
		}
	}

	if totalOwner < 8 {
		t.Fatalf("主人问的几轮里想起 owner 范围的条目合计只有 %d 条，题没有咬住东西", totalOwner)
	}
	// 邻居不变：群友那一档里群里的事和公开的事照常想得起
	peer := m.Recall(ctx, qqGroup(GA, false, peerA), Query{Text: "搬家 猫", Mode: ModeExplicit})
	got := map[string]bool{}
	for _, h := range peer.Hits {
		got[h.Ref] = true
	}
	if !got["f:4"] || !got["f:5"] {
		t.Errorf("群友再问，群里的事和公开的事该照常给出来：%v", got)
	}
	// 另一个群也能想起；主人私事标记仍区分私事与群内事。
	other := Render(m.Recall(ctx, qqGroup(GB, true, "owner"), Query{Text: "搬家 考试", Mode: ModeExplicit}), time.Now()).Lines(true)
	if l := lineWith(other, "搬家"); l == "" {
		t.Errorf("跨群缺少同角色原群记忆")
	}
	if l := lineWith(other, "主人下周要考试"); !strings.Contains(l, mark) {
		t.Errorf("另一个群里主人问，owner 的行也该带标记：%q", l)
	}
}
