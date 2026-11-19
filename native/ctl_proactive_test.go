package main

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/desksettings"
	"sumika-native/internal/paths"
	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
)

// proactive log 的后端：只读打开 go/trace.db 读状态和日志。临时数据目录，角色名是编的。

func proactiveShell(t *testing.T) (*ctlBackend, *store.Set) {
	t.Helper()
	dir := t.TempDir()
	p, err := paths.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	sh := &shell{}
	sh.settings.Store(desksettings.New(dir))
	return &ctlBackend{s: sh}, set
}

func TestProactiveLogBackendReadsStatesAndRows(t *testing.T) {
	t.Parallel()
	b, set := proactiveShell(t)
	ctx := context.Background()
	if l, err := b.ProactiveLog(ctx, 50); err != nil || len(l.Rows) != 0 || len(l.States) != 0 {
		t.Fatalf("还没有记录：%+v %v", l, err)
	}
	db := proactive.DBStore{DB: set.Trace()}
	now := time.Now()
	if err := db.SaveState(proactive.State{Character: "aa", Factor: 1.5, ColdStreak: 1, NextWake: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i, reason := range []string{"due", "snoozed", "owner-chatting"} {
		if _, err := db.AddRow(proactive.Row{At: now.Add(time.Duration(i) * time.Minute), Character: "aa", Kind: proactive.KindSkip, Reason: reason, Inputs: []byte(`{"factor":1}`), ModelNote: "备注"}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := b.ProactiveLog(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Rows) != 2 || l.Rows[0].Reason != "owner-chatting" || l.Rows[1].Reason != "snoozed" || l.Rows[0].Count != 1 || l.Rows[0].Note != "备注" || string(l.Rows[0].Inputs) != `{"factor":1}` {
		t.Fatalf("最近 2 行、新的在前：%+v", l.Rows)
	}
	if len(l.States) != 1 || l.States[0].Character != "aa" || l.States[0].Factor != 1.5 || l.States[0].NextWake == 0 || l.States[0].LastWake != 0 {
		t.Fatalf("状态：%+v", l.States)
	}
}

func TestProactiveLogBackendWithoutDataOrTable(t *testing.T) {
	t.Parallel()
	// 数据目录没就绪
	b := &ctlBackend{s: &shell{}}
	if _, err := b.ProactiveLog(context.Background(), 5); err == nil {
		t.Fatal("没数据目录应当报错")
	}
	// 有数据目录但还没有 go/trace.db
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	sh := &shell{}
	sh.settings.Store(desksettings.New(dir))
	b = &ctlBackend{s: sh}
	_, err := b.ProactiveLog(context.Background(), 5)
	if err == nil {
		t.Fatal("没有运行记录库应当报错")
	}
}
