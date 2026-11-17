package main

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// 两条正在读盘的流水线先停住，让测试能安排其后的可见／离屏请求顺序。
func blockedImageQueue(t *testing.T) (*imageStore, *uiQueue, <-chan string, chan<- struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	q := &uiQueue{}
	st := newImageStore(ctx, q.post)
	started := make(chan string, 16)
	release := make(chan struct{}, 2)
	st.fetch = func(ctx context.Context, ref api.MediaRef, _ int) ([]byte, error) {
		started <- ref.Hash
		if ref.Hash == "active-a" || ref.Hash == "active-b" {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []byte{1}, nil
	}
	bmp := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	st.decode = func([]byte, int) (*ui.Bitmap, error) { return bmp, nil }
	t.Cleanup(func() { cancel(); close(release); st.wg.Wait() })
	st.beginFrame()
	st.get(imgKey{"active-a", 128, false})
	st.get(imgKey{"active-b", 128, false})
	for range 2 {
		select {
		case <-started:
		case <-time.After(uiWaitLimit):
			t.Fatal("两条读取流水线没有启动")
		}
	}
	return st, q, started, release
}

func TestImageVisibleBeforeQueuedOffscreen(t *testing.T) {
	t.Parallel()
	st, q, started, release := blockedImageQueue(t)
	visible := false
	st.visible = func() bool { return visible }
	st.get(imgKey{"offscreen-old", 128, false})
	visible = true
	wanted := st.get(imgKey{"visible", 128, false})
	visible = false
	st.get(imgKey{"offscreen-new", 128, false})
	release <- struct{}{}
	select {
	case got := <-started:
		if got != "visible" {
			t.Fatalf("可见图排在离屏读取之后：先读了 %q", got)
		}
	case <-time.After(uiWaitLimit):
		t.Fatal("空出来的流水线没有开始读可见图")
	}
	release <- struct{}{}
	st.wg.Wait()
	q.drain()
	if wanted.state != imgOK {
		t.Fatalf("可见图没有交回界面：state=%d", wanted.state)
	}
}

func TestImageTransientRowsDoNotReadFiles(t *testing.T) {
	t.Parallel()
	st, q, started, release := blockedImageQueue(t)
	visible := false
	st.visible = func() bool { return visible }
	st.get(imgKey{"measurement-only", 128, false})
	// 列表量完高度后只保留真正的构建范围，测量用的行没有再次请求。
	st.beginFrame()
	visible = true
	wanted := st.get(imgKey{"visible", 128, false})
	q.drain()
	release <- struct{}{}
	release <- struct{}{}
	st.wg.Wait()
	q.drain()
	if len(started) != 1 {
		t.Fatalf("测量用的离屏行仍读了文件：后续读取 %d 次", len(started))
	}
	if got := <-started; got != "visible" || wanted.state != imgOK {
		t.Fatalf("可见图片没有完成：读了 %q，state=%d", got, wanted.state)
	}
	if st.peek(imgKey{"measurement-only", 128, false}) != nil {
		t.Fatal("测量用的离屏行留下了永久加载中的占位")
	}
}
