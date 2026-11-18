package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

func setLink(t *testing.T, a *App, ch api.LinkSettingsChange) {
	t.Helper()
	d := a.Desktop().Reader
	view, err := d.LinkSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SetLinkSettings(context.Background(), ch, view.Version); err != nil {
		t.Fatal(err)
	}
}

func linkSawFrames(peer *rejectPeer, url string) bool {
	return strings.Contains(strings.Join(peer.snapshot(), "\n"), url)
}

// 网站开关热生效：关了 X，下一条 X 链接不展开（记运行页）；重新打开，再下一条照常展开，全程不重启
func TestPlatformSwitchAppliesWithoutRestart(t *testing.T) {
	t.Parallel()
	e, a, peer := newRejectHost(t, 0, "", true)
	conn := sendLink(t, e, a, "971", "https://x.com/paper/status/74001")
	waitFor(t, "默认放开：第一条展开", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/74001") })

	setLink(t, a, api.LinkSettingsChange{Key: config.LinkPlatformEnabled, Site: "x", Bool: false})
	e.peer.send(t, conn, expandFrame(e, "972", testOwner, "https://x.com/paper/status/74002"))
	waitFor(t, "关掉的站记一行 blocked", func() bool {
		var n int
		err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM task_runs WHERE module='link' AND class='blocked' AND outcome='skipped'`).Scan(&n)
		return err == nil && n >= 1
	})
	time.Sleep(400 * time.Millisecond)
	if linkSawFrames(peer, "https://x.com/paper/status/74002") {
		t.Fatal("X 关了不该展开")
	}

	setLink(t, a, api.LinkSettingsChange{Key: config.LinkPlatformEnabled, Site: "x", Bool: true})
	e.peer.send(t, conn, expandFrame(e, "973", testOwner, "https://x.com/paper/status/74003"))
	waitFor(t, "重新打开后展开", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/74003") })
}

// 页面类型开关：关掉 X 的 post，不影响整站其他类型的判断；X 只有 post 一类，所以关掉后同样不展开
func TestKindSwitchBlocksThatKind(t *testing.T) {
	t.Parallel()
	e, a, peer := newRejectHost(t, 0, "", true)
	conn := sendLink(t, e, a, "981", "https://x.com/paper/status/75001")
	waitFor(t, "第一条展开", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/75001") })
	setLink(t, a, api.LinkSettingsChange{Key: config.LinkKindEnabled, Site: "x", Text: "post", Bool: false})
	e.peer.send(t, conn, expandFrame(e, "982", testOwner, "https://x.com/paper/status/75002"))
	time.Sleep(600 * time.Millisecond)
	if linkSawFrames(peer, "https://x.com/paper/status/75002") {
		t.Fatal("post 关了不该展开")
	}
}
