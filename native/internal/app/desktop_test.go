package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
)

func writeCard(t *testing.T, data, id, yml string) {
	t.Helper()
	dir := filepath.Join(data, "characters", id, "card")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "character.yml"), []byte(yml), 0600); err != nil {
		t.Fatal(err)
	}
}

// hashGo 把 go/ 下除共享内存索引以外的每个文件按内容算哈希；读路径不能改动任何一个。
func hashGo(t *testing.T, data string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(data, "go"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, "-shm") || strings.HasSuffix(p, "backend.lock") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDesktopArchiveReadOnlyAndCharacters(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "桌面 #%&")
	writeCard(t, opt.DataDir, "good", "name: 编造甲\ncolor: \"#8a7cff\"\nmodel: m1\n")
	writeCard(t, opt.DataDir, "nocolor", "name: 编造乙\nmodel: m2\n")
	writeCard(t, opt.DataDir, "nomodel", "name: 编造丙\ncolor: \"#123456\"\n")
	writeCard(t, opt.DataDir, "broken", "name: [不是名字\n")
	before := hashGo(t, opt.DataDir)
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	d := a.Desktop()
	defer d.Close()
	if a.Desktop() != d {
		t.Fatal("同一个 App 建了两份只读接口")
	}
	ctx := context.Background()
	sub, err := d.Reader.WatchOverview(ctx, api.OverviewRequest{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var snap api.Snapshot[api.Overview]
	select {
	case snap = <-sub.Updates():
	case <-time.After(10 * time.Second):
		t.Fatal("等概览超时")
	}
	sub.Close()
	if snap.Err != nil {
		t.Fatal(snap.Err)
	}
	got := map[string]api.Result[api.CharacterView]{}
	for _, c := range snap.Value.Characters.Items {
		got[c.Value.ID] = c
	}
	if c := got["good"]; c.State != api.Ready || c.Value.Name != "编造甲" || c.Value.Color != "#8a7cff" || c.Value.Model != "m1" || c.Value.Status != "只看历史" {
		t.Fatalf("好卡：%+v", c)
	}
	if c := got["nocolor"]; c.State != api.Ready || c.Value.Color != "" || c.Value.Model != "m2" {
		t.Fatalf("缺颜色：%+v", c)
	}
	if c := got["nomodel"]; c.State != api.Ready || c.Value.Model != "" || c.Value.Color != "#123456" {
		t.Fatalf("缺模型：%+v", c)
	}
	if c := got["broken"]; c.State != api.Failed || c.Err == nil || c.Value.Status != "读不到" {
		t.Fatalf("坏卡要单独报失败：%+v", c)
	}
	// 每个角色都有日常入口（没有消息也列），没卡的 console:invented 不冒出来
	if len(snap.Value.Conversations.Items) != 4 {
		t.Fatalf("会话：%+v", snap.Value.Conversations.Items)
	}
	key := snap.Value.Conversations.Items[1].Key
	if _, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 200}, ""); err != nil {
		t.Fatal(err)
	}
	if r, err := d.Reader.ReadSettings(ctx); err != nil || r.State != api.Ready {
		t.Fatalf("设置：%v", err)
	}
	d.Reader.ReadFaceIndex(ctx)
	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	after := hashGo(t, opt.DataDir)
	// SQLite 只读打开 WAL 库会建一个空的 -wal；新出现的文件只允许是这种空文件
	empty := hex.EncodeToString(func() []byte { s := sha256.Sum256(nil); return s[:] }())
	for p, h := range after {
		if _, ok := before[p]; !ok && !(strings.HasSuffix(p, "-wal") && h == empty) {
			t.Fatalf("读路径新写了 %s", p)
		}
	}
	for p, h := range before {
		if after[p] != h {
			t.Fatalf("读路径改了 %s", p)
		}
	}
}

func TestDesktopPublishStopsAfterClose(t *testing.T) {
	t.Parallel()
	a, err := Open(context.Background(), archiveFixture(t, "发布"))
	if err != nil {
		t.Fatal(err)
	}
	d := a.Desktop()
	d.Publish(api.RuntimeView{Mode: "archive", Phase: "ready", Reason: "开着"})
	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	d.Publish(api.RuntimeView{Mode: "archive", Phase: "late", Reason: "关了以后"})
	r := desktopread.NewResults(nil, nil, d.runtime)
	sub, err := r.WatchRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	v := <-sub.Updates()
	if v.Value.Phase != "ready" {
		t.Fatalf("关了以后还发布了：%+v", v.Value)
	}
	if _, err := d.Reader.WatchRuntime(context.Background()); err == nil {
		t.Fatal("关了以后还能订阅")
	}
}
