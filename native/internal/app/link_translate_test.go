package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
)

// 链接翻译的端到端：展开一条 X 帖，译文跟在原文后面；任何失败都只发原文，不写错误文案。
// 形状 × 入口：正文（英文、中文、日文、纯链接）× 模型回复（译文、报错、超时、没配模型）× 入口（QQ 群、QQ 私聊）× 开关（总开关、网站）。

const enText = "Artemis II has completed its lunar flyby and the crew is heading home."

// textFetcher 回一条指定正文的 X 短帖（取数其余照 picsFetcher）。
type textFetcher struct {
	picsFetcher
	text string
}

func (f textFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	r, err := f.picsFetcher.Fetch(ctx, l)
	r.Text = linkreader.Field[string]{State: "available", Value: f.text}
	r.Media = linkreader.Field[[]linkreader.Media]{State: "not_returned", Reason: "not_applicable"}
	return r, err
}

type fakeModel struct {
	mu     sync.Mutex
	calls  atomic.Int32
	system string
	user   string
	reply  func(ctx context.Context) (string, error)
}

func (m *fakeModel) call(ctx context.Context, system, user string) (string, error) {
	m.calls.Add(1)
	m.mu.Lock()
	m.system, m.user = system, user
	m.mu.Unlock()
	return m.reply(ctx)
}

func said(text string) *fakeModel {
	return &fakeModel{reply: func(context.Context) (string, error) { return text, nil }}
}

func newTranslateHost(t *testing.T, text string, m *fakeModel) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	e, a, peer := newRejectHostWith(t, textFetcher{picsFetcher: pics(), text: text})
	if m != nil {
		a.opt.LinkTranslateCall = m.call
	}
	return e, a, peer
}

func taskRunCount(t *testing.T, a *App, where string) int {
	t.Helper()
	var n int
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM task_runs WHERE module='link' AND ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLinkTranslateAppendsAfterOriginal(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		m := said("阿尔忒弥斯二号完成了绕月飞行，乘组正在返程。")
		e, a, peer := newTranslateHost(t, enText, m)
		conn := startSend(t, e, a)
		scriptLink(t, e, a, conn, entry, "9101", "https://x.com/paper/status/77001")
		waitFor(t, "译文发出", func() bool { return linkSawFrames(peer, "绕月飞行") })
		all := strings.Join(peer.snapshot(), "\n")
		if strings.Contains(all, "Artemis II has completed") || !strings.Contains(all, "（翻译自英语）") {
			t.Fatalf("只放译文加翻译自、不留原文（规格-链接发送改版 B13）：%s", all)
		}
		if m.calls.Load() != 1 || !strings.Contains(m.user, enText) || !strings.Contains(m.system, "DATA to translate") {
			t.Fatalf("调用不对：%d 次 %q", m.calls.Load(), m.user)
		}
	})
}

func TestLinkTranslateFailureSendsOriginalOnly(t *testing.T) {
	t.Parallel()
	boom := &fakeModel{reply: func(context.Context) (string, error) { return "", errors.New("网关 502") }}
	e, a, peer := newTranslateHost(t, enText, boom)
	sendLink(t, e, a, "9102", "https://x.com/paper/status/77002")
	waitFor(t, "原文发出", func() bool { return linkSawFrames(peer, "Artemis II has completed") })
	time.Sleep(300 * time.Millisecond)
	all := strings.Join(peer.snapshot(), "\n")
	for _, bad := range []string{"没解析出来", "翻译失败", "502", "译文"} {
		if strings.Contains(all, bad) {
			t.Fatalf("失败时不该出现 %q：%s", bad, all)
		}
	}
	waitFor(t, "运行页记一行翻译失败", func() bool { return taskRunCount(t, a, "class='translate' AND outcome='failed'") == 1 })
}

func TestLinkTranslateTimeoutDoesNotHoldExpansion(t *testing.T) {
	t.Parallel()
	slow := &fakeModel{reply: func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
	e, a, peer := newTranslateHost(t, enText, slow)
	setLink(t, a, api.LinkSettingsChange{Key: config.LinkTranslateTimeout, Int: 1})
	start := time.Now()
	sendLink(t, e, a, "9103", "https://x.com/paper/status/77003")
	waitFor(t, "超时后只发原文", func() bool { return linkSawFrames(peer, "Artemis II has completed") })
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("超时后拖了 %v", d)
	}
	waitFor(t, "运行页记一行超时", func() bool { return taskRunCount(t, a, "class='translate' AND outcome='timeout'") == 1 })
}

func TestLinkTranslateNotCalledWhenNotNeeded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, text string
		change     *api.LinkSettingsChange
	}{
		{"中文正文", "今天天气很好，纸船试航成功了。", nil},
		{"纯链接", "https://example.invalid/a/b", nil},
		{"总开关关", enText, &api.LinkSettingsChange{Key: config.LinkTranslateEnabled, Bool: false}},
		{"网站关", enText, &api.LinkSettingsChange{Key: config.LinkPlatformTranslate, Site: "x", Bool: false}},
		{"模板没有译文字段", enText, &api.LinkSettingsChange{Key: config.LinkTemplate, Site: "x", Text: "$作者\n$原文\n$链接"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := said("不该出现的译文")
			e, a, peer := newTranslateHost(t, c.text, m)
			if c.change != nil {
				setLink(t, a, *c.change)
			}
			if c.name == "模板没有译文字段" {
				setLink(t, a, api.LinkSettingsChange{Key: config.LinkSiteForward, Site: "x", Text: "never"})
			}
			id := "920" + string(rune('0'+i))
			sendLink(t, e, a, id, "https://x.com/paper/status/7710"+string(rune('0'+i)))
			waitFor(t, "展开发出", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/7710"+string(rune('0'+i))) })
			if m.calls.Load() != 0 || linkSawFrames(peer, "不该出现的译文") {
				t.Fatalf("不该调模型：%d 次", m.calls.Load())
			}
		})
	}
}

// 没配后台模型（默认测试主机就是）：原文照发，不记运行页
func TestLinkTranslateNoBackgroundModelIsSilent(t *testing.T) {
	t.Parallel()
	e, a, peer := newTranslateHost(t, enText, nil)
	sendLink(t, e, a, "9104", "https://x.com/paper/status/77004")
	waitFor(t, "展开发出", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/77004") })
	time.Sleep(300 * time.Millisecond)
	if n := taskRunCount(t, a, "class='translate'"); n != 0 {
		t.Fatalf("没配模型不该记运行页，记了 %d 行", n)
	}
}

// 同一条链接再发一次：命中缓存，不再调模型
func TestLinkTranslateCachedByCleanURL(t *testing.T) {
	t.Parallel()
	m := said("阿尔忒弥斯二号完成了绕月飞行，乘组正在返程。")
	e, a, peer := newTranslateHost(t, enText, m)
	conn := sendLink(t, e, a, "9105", "https://x.com/paper/status/77005")
	waitFor(t, "第一次译文", func() bool { return linkSawFrames(peer, "绕月飞行") })
	e.peer.send(t, conn, expandFrame(e, "9106", testOwner, "https://x.com/paper/status/77005?s=20"))
	waitFor(t, "第二次也展开", func() bool { return strings.Count(strings.Join(peer.snapshot(), "\n"), "绕月飞行") >= 2 })
	if m.calls.Load() != 1 {
		t.Fatalf("第二次应命中缓存，调用 %d 次", m.calls.Load())
	}
}
