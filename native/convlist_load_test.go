package main

import (
	"testing"
	"time"
)

// 会话列表第一份概览到之前的提示（loadstate 的规则）。

// 会话列表：第一份概览到之前不写"还没有…"（那是"确实没有"的话，不是"还没到"）；读得慢了写"取数据中…"；到了以后没有才写没有
func TestLoadHintConversationListWaitsForOverview(t *testing.T) {
	for _, rail := range []int{0, 1} {
		a := &app{rail: rail}
		a.ovLoad.Begin()
		tt := testerOf(400, 500, a.convList)
		bad := []string{"还没有本地会话", "还没有 QQ 会话", "还没有群聊", "取数据中"}
		for i := 0; i < 5; i++ {
			for _, s := range bad {
				if tt.HasText(s) {
					t.Fatalf("rail=%d 第 %d 帧（%d 毫秒）就写了 %q：%v", rail, i, i*50, s, tt.Texts())
				}
			}
			tt.Advance(50 * time.Millisecond)
			tt.Frame()
		}
		tt.Advance(100 * time.Millisecond)
		tt.Frame()
		if !tt.HasText("取数据中…") || tt.HasText("还没有本地会话") || tt.HasText("还没有 QQ 会话") || tt.HasText("还没有群聊") {
			t.Fatalf("rail=%d 过了阈值应只写取数据中：%v", rail, tt.Texts())
		}
		a.ovLoad.Succeed()
		tt.Frame()
		want := "还没有 QQ 会话"
		if rail == 0 {
			want = "还没有本地会话"
		}
		if !tt.HasText(want) || tt.HasText("取数据中") {
			t.Fatalf("rail=%d 到了以后应写 %q：%v", rail, want, tt.Texts())
		}
	}
}
