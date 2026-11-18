package channel

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func TestMediaOwnershipLifecycle(t *testing.T) {
	t.Parallel()
	caps := actionCaps()
	caps.MediaGroup = types.MediaCaps{Image: true, ImageBase64: true}
	h := newOH(t, caps, allowActionGuard)
	h.run(nil)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	exited := make(chan struct{})
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		<-release
		defer close(exited)
		if !c.Part.Body[1].Media.Available() {
			t.Error("适配器退出前媒体已释放")
		}
		return fake.Confirmed(c.Part.ID, "-90077", c.At), nil
	})
	path := filepath.Join(t.TempDir(), "编的-owned.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("owned-content")); err != nil {
		t.Fatal(err)
	}
	var released atomic.Int32
	media, err := types.FileMedia("owned-fixture", "image/png", f, func() error { released.Add(1); return os.Remove(path) })
	if err != nil {
		t.Fatal(err)
	}
	p := types.Part{Kind: types.PartMessage, Quote: "-71", Body: []types.BodySegment{{Kind: types.BodyText, Text: "组合文字"}, {Kind: types.BodyImage, Media: media}}}
	held, err := h.out.sendHeld(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{p}, []*types.MediaResource{media}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = held.Start(); err != nil {
		t.Fatal(err)
	}
	id := held.PlanID
	h.waitSent(1)
	h.clk.WaitTimer(t, 30*time.Second)
	h.clk.Advance(30 * time.Second)
	h.waitPlan(id, store.PlanUnknown)
	if !media.Available() || released.Load() != 0 {
		t.Fatal("unknown 时 Write 尚未退出却释放了媒体")
	}
	select {
	case <-held.Done:
		t.Fatal("Write 仍在读取时提前归还生产者名额")
	default:
	}
	echo := echoOf(acctA, group("g1"), "-90078", "组合文字")
	other := echo
	other.Segs = append(slices.Clone(echo.Segs), types.Seg{Kind: types.SegImage, File: "0123456789abcdef0123456789abcdef.png"})
	if ok, err := h.out.ClaimEcho(other); err != nil || ok {
		t.Fatalf("不同图片串认：%v %v", ok, err)
	}
	// 只剩文字的回声认领，记成吞图（规格-链接发送改版 A1；旧规矩是不认，带图回声入库会多存一条）
	if ok, err := h.out.ClaimEcho(echo); err != nil || !ok {
		t.Fatalf("只剩文字的回声该认领成吞图：%v %v", ok, err)
	}
	if n, ok := h.out.AwaitPart(context.Background(), "", id, 0, 0); !ok || !n.MediaMissing {
		t.Fatalf("要记成吞图：%+v %v", n, ok)
	}
	close(release)
	<-exited
	eventually(t, "Write退出后释放文件", func() bool { return released.Load() == 1 && !media.Available() })
	select {
	case <-held.Done:
	case <-time.After(time.Second):
		t.Fatal("资源释放后未通知生产者")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("文件残留", err)
	}
	if h.plan(id).State != store.PlanDone {
		t.Fatal("迟到回包覆盖了回声认领的确认")
	}
}
