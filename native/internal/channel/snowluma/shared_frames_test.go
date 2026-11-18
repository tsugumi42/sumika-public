package snowluma

// 共用场景（testdata/shared/qq-frames，格式见该目录 README.md）：协议端推来的一帧原文 -> 翻成什么（消息 / 通知 / 状态 / 丢弃）。
// SUMIKA_SHARED_UPDATE=1 把实际结果写回各文件的 expect。

import (
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/sharedtest"
)

type qfInbound struct {
	Kind       string      `json:"kind"`
	Account    string      `json:"account"`
	Conv       string      `json:"conv"`
	PlatformID string      `json:"platform_id"`
	FromID     string      `json:"from_id"`
	FromNick   string      `json:"from_nick"`
	FromCard   string      `json:"from_card"`
	TimeUnix   int64       `json:"time_unix"`
	Segs       []types.Seg `json:"segs"`
}
type qfNotice struct {
	Kind     string `json:"kind"`
	Account  string `json:"account"`
	Conv     string `json:"conv"`
	TimeUnix int64  `json:"time_unix"`
	Actor    string `json:"actor"`
	Target   string `json:"target"`
	MsgID    string `json:"msg_id"`
	Ref      string `json:"ref"`
	Raw      string `json:"raw"`
}
type qfStatus struct {
	Kind       string `json:"kind"`
	Account    string `json:"account"`
	TimeUnix   int64  `json:"time_unix"`
	IntervalMS int64  `json:"interval_ms"`
	Reason     string `json:"reason"`
	Conv       string `json:"conv"`
}
type qfOut struct {
	Dropped bool       `json:"dropped"` // 无效帧或不翻的帧：什么也不交出去
	Inbound *qfInbound `json:"inbound,omitempty"`
	Notice  *qfNotice  `json:"notice,omitempty"`
	Status  *qfStatus  `json:"status,omitempty"`
}
type qfCase struct {
	Name   string `json:"name"`
	Frame  string `json:"frame"` // 帧原文（字符串，保住大整数和坏 JSON）
	Expect qfOut  `json:"expect"`
}
type qfFile struct {
	ID      string   `json:"id"`
	About   string   `json:"about"`
	Sources []string `json:"source_tests"`
	Cases   []qfCase `json:"cases"`
}

func qfUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0 // 帧里没有时间；接收端（Run）用收到的时间补，翻译这一步给零值
	}
	return t.Unix()
}

func qfConv(c types.Conversation) string {
	if c.Channel == "" {
		return "" // 没有会话（整个账号的通知）
	}
	return c.String()
}

func qfRun(frame string) qfOut {
	in, n, st, err := translate([]byte(frame))
	var out qfOut
	if err != nil || (in == nil && n == nil && st == nil) {
		out.Dropped = true
		return out
	}
	if in != nil {
		segs := in.Segs
		if segs == nil {
			segs = []types.Seg{}
		}
		out.Inbound = &qfInbound{in.Kind, in.Account, in.Conv.String(), in.PlatformID, in.From.ID, in.From.Nick, in.From.Card, qfUnix(in.Time), segs}
	}
	if n != nil {
		out.Notice = &qfNotice{n.Kind, n.Account, qfConv(n.Conv), qfUnix(n.Time), n.Actor, n.Target, n.MsgID, n.Ref, n.Raw}
	}
	if st != nil {
		out.Status = &qfStatus{st.Kind, st.Account, qfUnix(st.Time), st.Interval.Milliseconds(), st.Reason, qfConv(st.Conv)}
	}
	return out
}

func TestSharedFrames(t *testing.T) {
	for _, path := range sharedtest.Files(t, "qq-frames") {
		var f qfFile
		sharedtest.Load(t, path, &f)
		t.Run(f.ID, func(t *testing.T) {
			if sharedtest.Update() {
				for i := range f.Cases {
					f.Cases[i].Expect = qfRun(f.Cases[i].Frame)
				}
				sharedtest.Save(t, path, f)
				return
			}
			for _, c := range f.Cases {
				if ok, want, got := sharedtest.Same(c.Expect, qfRun(c.Frame)); !ok {
					t.Errorf("%s 不同\n期望：%s\n实际：%s", c.Name, want, got)
				}
			}
		})
	}
}
