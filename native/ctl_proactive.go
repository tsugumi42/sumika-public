package main

// sumika-ctl proactive log（规格-主动联系 第十节第 4 条）：只读打开 trace.db，读导演的状态和最近的日志行。
// 每次请求打开一次，用完就关；表里只有原因码、数字和模型自己写的一句理由，不含聊天原文。

import (
	"context"
	"os"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/paths"
	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
)

var _ ctlserver.ProactiveBackend = (*ctlBackend)(nil)

func (b *ctlBackend) ProactiveLog(ctx context.Context, n int) (ctlproto.ProactiveLog, error) {
	ds := b.s.settings.Load()
	if ds == nil {
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "数据目录打不开")
	}
	file := p.GoTraceDB()
	if _, err := os.Stat(file); err != nil {
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "还没有运行记录库（go/trace.db）")
	}
	db, err := store.OpenReadOnlyDB(file)
	if err != nil {
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "打不开运行记录库")
	}
	defer db.Close()
	s := proactive.DBStore{DB: db}
	out := ctlproto.ProactiveLog{States: []ctlproto.ProactiveState{}, Rows: []ctlproto.ProactiveRow{}}
	states, err := s.States()
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil // 库还没升到有这两张表的版本（程序还没带着新版本启动过）：按没有记录处理
		}
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "读主动联系状态出错")
	}
	rows, err := s.Recent(n)
	if err != nil {
		return ctlproto.ProactiveLog{}, ctlproto.Errorf(ctlproto.CodeFailed, "读主动联系日志出错")
	}
	for _, st := range states {
		out.States = append(out.States, ctlproto.ProactiveState{
			Character: st.Character, Factor: st.Factor, ColdStreak: st.ColdStreak,
			NextWake: msOf(st.NextWake), LastWake: msOf(st.LastWake), QuietUntil: msOf(st.QuietUntil),
		})
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, ctlproto.ProactiveRow{
			ID: r.ID, At: msOf(r.At), Character: r.Character, Kind: string(r.Kind), Reason: r.Reason, Inputs: r.Inputs,
			NextAt: msOf(r.NextAt), Thread: r.Thread, Note: r.ModelNote, Count: r.Count,
		})
	}
	return out, nil
}

// msOf 零值时间是 0，其余是毫秒时间戳
func msOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
