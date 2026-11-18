package main

import (
	"fmt"
	"strings"
	"sumika-native/internal/api"
	"testing"
)

func withImage(v api.MessageView, hash string) api.MessageView {
	v.Segments = []api.DisplaySegment{{Kind: "image", Width: 300, Height: 200, Media: &api.MediaRef{Source: v.Ref, Hash: hash}}}
	v.Text = "[图片]"
	return v
}

// 每条消息都有自己的时间，短文同行、长文不与信息区重叠；两种私聊没有名字行。
func TestMessageTimeAndIdentity(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, mine := range []bool{false, true} {
			t.Run(fmt.Sprintf("group=%v/mine=%v", group, mine), func(t *testing.T) {
				ms := msgsOf(t, view("a", "human", "编造发送人", "短文", t0), view("b", "human", "编造发送人", strings.Repeat("中文 English 混排正文 ", 20), t0+60000))
				for _, m := range ms {
					m.mine = mine
				}
				a := chatApp(ms, group)
				tt, p := chatTester(a, 900, 700)
				count := 0
				for _, s := range tt.Texts() {
					if s == "Date text" {
						count++
					}
				}
				if count != 2 {
					t.Fatalf("逐条时间应为2，得到%d: %v", count, tt.Texts())
				}
				for _, id := range []string{"a", "b"} {
					b := p.box(t, "body:"+id+":0")
					if b.W > 408.5 || b.X < 0 || b.X+b.W > 900 {
						t.Fatalf("正文越界 %+v", b)
					}
				}
				names := 0
				for _, s := range tt.Texts() {
					if s == "编造发送人" {
						names++
					}
				}
				want := 0
				if group && !mine {
					want = 1
				}
				if names != want {
					t.Fatalf("组首名字=%d want=%d", names, want)
				}
				if group && !mine {
					r := p.box(t, "body:a:0")
					if r.X != 87 {
						t.Fatalf("头像步进后的正文左沿=%v want=87", r.X)
					}
				}
			})
		}
	}
}

func TestMessageNarrowInfoDoesNotOverlapReactions(t *testing.T) {
	m := msgsOf(t, withReactions(view("n", "human", "编造发送人", strings.Repeat("内容", 20), t0), rv("r", "128077", "human", "qq:9", "编造回应人")))[0]
	a := chatApp([]*msg{m}, true)
	tt, p := chatTester(a, 380, 600)
	pill := p.box(t, "react:n:128077")
	date, ok := tt.Find("Date text")
	if !ok || rectsOverlap(pill, date) {
		t.Fatalf("回应压时间：pill=%+v date=%+v", pill, date)
	}
}
