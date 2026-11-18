package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/memory/memtest"
	"sumika-native/internal/memview"
)

// 桌面的记忆页读服务装配（规格-记忆页 4.1）：每个有卡的角色一份视图，没有 memory.db 的角色 HasDB 为假；只读，不改库一个字节；
// Desktop.Close 以后读服务也关。数据全是编的，临时目录。
func TestDesktopMemoryReader(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "记忆页装配")
	writeCard(t, opt.DataDir, "a", "name: 编造甲\n")
	writeCard(t, opt.DataDir, "b", "name: 编造乙\n")
	file := filepath.Join(opt.DataDir, "go", "characters", "a", "memory.db")
	memtest.BunShaped(t, file)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('F1', 'fact', '编的事实', 'owner', 'private', 'active', 1000)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	sum := func() string {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	before := sum()

	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	if d.Memory == nil {
		t.Fatal("桌面接口没带记忆页的读服务")
	}
	ctx := context.Background()
	sub, err := d.Memory.WatchMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var snap api.Snapshot[api.MemoryOverview]
	select {
	case snap = <-sub.Updates():
	case <-time.After(10 * time.Second):
		t.Fatal("等记忆概览超时")
	}
	sub.Close()
	if snap.Err != nil || len(snap.Value.Characters) != 2 {
		t.Fatalf("概览 %+v", snap)
	}
	got := map[string]api.MemoryCharacter{}
	for _, c := range snap.Value.Characters {
		got[c.ID] = c
	}
	if c := got["a"]; !c.HasDB || c.Total != 1 || c.Name != "编造甲" || c.ConfirmNeed != 3 || c.Migrated {
		t.Errorf("有库的角色 %+v", c)
	}
	if c := got["b"]; c.HasDB || c.Err != "" || c.Total != 0 {
		t.Errorf("没有库的角色 %+v", c)
	}
	r, err := d.Memory.ListMemory(ctx, api.MemoryListRequest{Character: "a"})
	if err != nil || len(r.Value.Items) != 1 {
		t.Fatalf("列表 %+v %v", r.Value, err)
	}
	if _, err := d.Memory.ListMemory(ctx, api.MemoryListRequest{Character: "nobody"}); !errors.Is(err, memview.ErrParam) {
		t.Errorf("不认识的角色：%v", err)
	}
	d.Close()
	if _, err := d.Memory.ListMemory(ctx, api.MemoryListRequest{Character: "a"}); !errors.Is(err, memview.ErrClosed) {
		t.Errorf("桌面接口关了以后读服务也该关：%v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if after := sum(); after != before {
		t.Error("读记忆页动了 memory.db")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if fi, err := os.Stat(file + suffix); err == nil && fi.Size() > 0 && suffix == "-wal" {
			t.Errorf("读记忆页留下了非空的 %s", suffix)
		}
	}
}
