package main

import (
	"strings"
	"testing"
	"time"
)

// 工作台端到端（规格-过程组件 3.7、ADR-286）：她的回复用 markdown 逐字出现，出字时这条没有时间，出完才有；
// 中途停下时已出的字留着、时间立刻显示。
func TestViewStreamingReply(t *testing.T) {
	for _, stop := range []bool{false, true} {
		a := newApp()
		tt := tester(a)
		runScene(t, a, tt, "让她回一句")
		cv := a.cv()
		tt.Advance(450 * time.Millisecond) // 300ms 后开始出字，50ms 一个字
		reply := cv.Messages[len(cv.Messages)-1]
		if reply.State != "streaming" {
			t.Fatalf("停=%v 回复没在出字：%s", stop, reply.State)
		}
		row := geometryTree(tt).Find(reply.ID)
		if row == nil || row.Find("md") == nil {
			t.Fatalf("停=%v 回复没用 markdown 画", stop)
		}
		if row.Find("time") != nil {
			t.Errorf("停=%v 出字时显示了时间", stop)
		}
		if stop {
			a.E.stop(cv.ID)
			tt.Frame()
			tt.Frame()
			if r := geometryTree(tt).Find(reply.ID); r == nil || r.Find("time") == nil || !strings.Contains(strings.Join(tt.Texts(), ""), reply.Text) {
				t.Errorf("停下以后时间没立刻出来，或者已出的字没了")
			}
			continue
		}
		tt.Advance(3 * time.Second)
		if reply.State != "complete" {
			t.Fatalf("回复没出完：%s", reply.State)
		}
		if r := geometryTree(tt).Find(reply.ID); r == nil || r.Find("time") == nil {
			t.Errorf("出完以后没有时间")
		}
	}
}
