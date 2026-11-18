package memwrite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/memory"
	"testing"
)

func TestModelSummaryTransition(t *testing.T) {
	f := newFx(t)
	f.in.Cfg.Summarize = []string{"suzu"}
	f.in.Cfg.CatchUp = "latest"
	f.in.HasModel = false
	f.events.heads = []memory.ConvHead{{Conversation: "console:suzu", LastSeq: 15}}
	m := f.run()
	if !m.Writable() || m.SummarizeEnabled() {
		t.Fatal("缺后台模型时只开 remember")
	}
	if _, ok, err := m.Meta(context.Background(), memory.MetaSummarizeEnabledAt); err != nil || ok {
		t.Fatalf("提前写了元数据 %v", err)
	}
	f.in.HasModel = true
	if err := EnableSummary(context.Background(), f.in, m); err != nil {
		t.Fatal(err)
	}
	if !m.SummarizeEnabled() {
		t.Fatal("未发布整理开关")
	}
	at, ok, err := m.Meta(context.Background(), memory.MetaSummarizeEnabledAt)
	if err != nil || !ok || at != f.in.now().UnixMilli() {
		t.Fatalf("元数据 %d %v %v", at, ok, err)
	}
	files, err := os.ReadDir(f.backup)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range files {
		if strings.Contains(entry.Name(), "summarize") {
			found = true
			info, err := os.Stat(filepath.Join(f.backup, entry.Name()))
			if err != nil || info.Size() == 0 {
				t.Fatal("备份为空")
			}
		}
	}
	if !found {
		t.Fatalf("没有整理前备份 %v", files)
	}
	m.SetSummaryEnabled(false, false)
	if m.SummarizeEnabled() || m.AutoConfirming() || !m.Writable() {
		t.Fatal("关整理改变了写入授权")
	}
}
func TestModelSummaryPrerequisites(t *testing.T) {
	for _, kind := range []string{"not-listed", "no-catchup", "backup-fails", "catchup-fails", "read-only"} {
		t.Run(kind, func(t *testing.T) {
			f := newFx(t)
			f.in.Cfg.Summarize = []string{"suzu"}
			f.in.Cfg.CatchUp = "latest"
			f.in.HasModel = false
			if kind == "read-only" {
				f.in.Cfg.Characters = nil
			}
			m := f.run()
			f.in.HasModel = true
			switch kind {
			case "not-listed":
				f.in.Cfg.Characters = nil
			case "no-catchup":
				f.in.Cfg.CatchUp = ""
			case "backup-fails":
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				f.in.BackupDir = file
			case "catchup-fails":
				f.events.err = context.Canceled
			}
			if err := EnableSummary(context.Background(), f.in, m); err == nil {
				t.Fatal("缺前提却开了整理")
			}
			if m.SummarizeEnabled() {
				t.Fatal("失败后仍开着")
			}
		})
	}
}
