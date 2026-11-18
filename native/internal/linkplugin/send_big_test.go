package linkplugin

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/plugintrace"
)

// 大图上传慢、回声晚（用户 2026-11-13，规格-链接发送改版 B12 第 4 条）：pixiv 漫画原图 15.8 MB，上传要十几秒，
// 以前固定等 2 秒回声，等不到就判成被拒、转 180° 重发，原来那张后来又到了，群里有两份，还记下这张图“被拒”6 小时。
// B12：等回声的时间加上按体积算的上传时间；等满了还没回声的大图按结果不明处理。
// B15（用户 2026-11-14）：4 次大图没回声之后一次晚到的都没有，是被 QQ 吞了，结果不明让群里什么也没有却贴了“完成”；
// 所以大图等回声放宽到 30 秒上下封顶（types.EchoAllowance），等满了照样当没送到、走换形式的阶梯，
// 原来那份的回声晚到由 late.go 的看守撤回补发的那份（late_test.go）。小图照旧。

var (
	bigPictureOnce  sync.Once
	bigPictureBytes []byte
)

// bigPicture 是一张编的噪点 PNG，体积超过大图的线（噪点压不下去）；字节只编一次（编一张要几百毫秒，-race 下更慢），每次给一份新资源
func bigPicture(t *testing.T) *types.MediaResource {
	t.Helper()
	bigPictureOnce.Do(func() {
		rng := rand.New(rand.NewSource(7))
		im := image.NewNRGBA(image.Rect(0, 0, 1200, 900))
		for i := 0; i < len(im.Pix); i += 4 {
			v := uint32(rng.Int63())
			im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = byte(v), byte(v>>8), byte(v>>16), 255
		}
		im.Set(0, 0, color.NRGBA{R: 255, A: 255})
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			panic(err)
		}
		bigPictureBytes = b.Bytes()
	})
	m := types.MemoryMedia("big", "image/png", 1200, 900, bytes.Clone(bigPictureBytes))
	if m.Size < types.BigMediaBytes {
		t.Fatalf("编的大图只有 %d 字节，不够大", m.Size)
	}
	return m
}

// 回声晚了几秒到（比默认的 2 秒晚，比按体积放宽后的时间早）：大图该等到它，算送到了，不转 180° 重发。放回旧的固定 2 秒就红：第二帧是转过的图
func TestBigImageLateEchoIsNotRejected(t *testing.T) {
	t.Parallel()
	pic := bigPicture(t)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			r := unconfirmed(f.clock)
			r.echo = echoed(f.clock, "m1", false).echo
			return r
		}
		return ok("m2")
	}, nil)
	f.out.echoNeeds = 8 * time.Second
	rep := f.deliver(nil, textAndPics("编的正文", pic))
	s := f.out.sent()
	if len(s) != 1 || s[0].images[0] != orig {
		t.Fatalf("回声 8 秒后到，大图该等到它，只发一次原图：%d 帧 %+v", len(s), s)
	}
	if want := 2*time.Second + types.EchoAllowance(pic.Size); len(f.out.waits) != 1 || f.out.waits[0] != want {
		t.Fatalf("等回声该是 2 秒加放宽量 %v：%v", want, f.out.waits)
	}
	if len(f.recalls) != 0 || !rep.delivered {
		t.Fatalf("不该撤回，该算送到了：recalls=%v delivered=%v", f.recalls, rep.delivered)
	}
}

// 大图等满放宽后的时间还是没有回声：和小图一样当没送到，走换形式的阶梯（B15，取代 B12 的“结果不明”）：
// 先转 180° 重发，转过的也没回声再退文字版，每一级都等满；按图记“被拒”的结论。
// 改坏检查：把 echo.go 的大图分支改回“按结果不明返回”，这条就红（只发一帧、没有文字版）
func TestBigImageNoEchoGoesRotatedThenText(t *testing.T) {
	t.Parallel()
	pic := bigPicture(t)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if len(p.images) > 0 {
			return unconfirmed(f.clock) // 原图和转过的图都没回声（被 QQ 吞了）
		}
		return okSent("mt", f.clock)
	}, nil)
	rep := f.deliver(nil, textAndPics("编的正文", pic))
	s := f.out.sent()
	if len(s) != 3 || s[1].images[0] == orig || len(s[1].images) != 1 || s[1].text != "编的正文" || len(s[2].images) != 0 {
		t.Fatalf("没回声照常走阶梯：原图、转 180° 的图文、文字版：%+v", s)
	}
	want := 2*time.Second + types.EchoAllowance(pic.Size)
	if len(f.out.waits) != 2 || f.out.waits[0] != want || f.out.waits[1] != want {
		t.Fatalf("原图和转过的图各等 %v：%v", want, f.out.waits)
	}
	if want < 20*time.Second || want > 30*time.Second {
		t.Fatalf("大图等回声该在 20～30 秒之间：%v", want)
	}
	if !rep.delivered || !rep.landed || !rep.fallback || rep.unsure {
		t.Fatalf("文字版确认了：送到、退了文字版、不是结果不明：%+v", rep)
	}
	if f.r.p.verdicts.empty() {
		t.Fatalf("原图没回声照旧记“这张图直接发被拒”的结论")
	}
	tagged := 0
	for _, e := range f.rows(RouteEcho) {
		if e.Result == plugintrace.Fail && e.Kind == kindEchoTimeout {
			tagged++
		}
	}
	if tagged != 2 {
		t.Fatalf("两次没等到回声各记一行 echo_timeout：%+v", f.rows(RouteEcho))
	}
}

// 小图照旧：没回声按没送到、转 180° 重发（按图的结论照旧记，同一张图下次直接用转图，见 app 的 TestVerdictsRememberAbsentButNotUnknown）；
// 等回声的时长只多了按体积算的那一点：不到 256 KiB 的图一个字节都不加。
func TestSmallImageNoEchoStillRotates(t *testing.T) {
	t.Parallel()
	pic := picture(t, 2)
	orig := digest(t, pic)
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return ok("m2")
	}, nil)
	f.deliver(nil, textAndPics("编的正文", pic))
	if s := f.out.sent(); len(s) != 2 || s[1].images[0] == orig {
		t.Fatalf("小图没回声照旧转图重发：%+v", s)
	}
	if len(f.out.waits) == 0 || f.out.waits[0] != 2*time.Second {
		t.Fatalf("小图等回声照旧 2 秒：%v", f.out.waits)
	}
}

// 等回声的放宽量（types.EchoAllowance）：小于 2 MiB 的和 UploadAllowance 一样；大图每 MiB 加 9 秒、上限 28 秒
func TestEchoAllowanceBySize(t *testing.T) {
	t.Parallel()
	for _, n := range []int64{0, 1 << 10, 256 << 10, 512 << 10, 1 << 20, 2<<20 - 1} {
		if got, want := types.EchoAllowance(n), types.UploadAllowance(n); got != want {
			t.Errorf("%d 字节（小图）该和 UploadAllowance 一样 %v：%v", n, want, got)
		}
	}
	for n, want := range map[int64]time.Duration{
		2 << 20:  15750 * time.Millisecond,
		3 << 20:  24750 * time.Millisecond,
		4 << 20:  types.MaxEchoAllowance,
		16 << 20: types.MaxEchoAllowance,
		1 << 40:  types.MaxEchoAllowance,
	} {
		if got := types.EchoAllowance(n); got != want {
			t.Errorf("%d 字节：要 %v，得 %v", n, want, got)
		}
	}
	if total := 2*time.Second + types.MaxEchoAllowance; total != 30*time.Second {
		t.Errorf("默认基础值加上限该是 30 秒：%v", total)
	}
}

// 十六 MB 的原图（pixiv 漫画那张 15.8 MB 的量级）：图字节经备份克隆以后体积还在，放宽量按原体积算，不是 0，并封顶 30 秒。
// 不是合法 PNG，转不了图：没回声按没送到走阶梯，转图没准备成，直接退文字版。
func TestHugeImageKeepsSizeThroughBackup(t *testing.T) {
	t.Parallel()
	huge := types.MemoryMedia("huge", "image/png", 4961, 7016, bytes.Repeat([]byte{7, 8, 9}, 16<<20/3))
	var f *flow
	f = newFlow(t, func(_ int, p sentPart) echoReply {
		if len(p.images) > 0 {
			return unconfirmed(f.clock)
		}
		return okSent("mt", f.clock)
	}, nil)
	rep := f.deliver(nil, textAndPics("编的正文", huge))
	want := 2*time.Second + types.EchoAllowance(huge.Size)
	if len(f.out.waits) != 1 || f.out.waits[0] != want || want != 30*time.Second {
		t.Fatalf("16 MB 的图等回声该封顶在 30 秒（%v）：%v", want, f.out.waits)
	}
	if s := f.out.sent(); len(s) != 2 || len(s[1].images) != 0 || !rep.fallback || !rep.landed {
		t.Fatalf("转不了图，没回声直接文字版：%d 帧 %+v %+v", len(s), s, rep)
	}
}

// 开着 historyCheck 时，大图“历史里没找到”也按没送到换形式（B15，取代 B12 的“只算结果不明”）；小图一样
func TestBigImageHistoryAbsentGoesRotated(t *testing.T) {
	t.Parallel()
	on := func(e *config.LinkExpand) { e.Send.HistoryCheck = true }
	absent := func(context.Context, types.Conversation, string) (types.AfterPage, error) {
		return types.AfterPage{ReachedLatest: true}, nil
	}
	run := func(pic func() *types.MediaResource) []sentPart {
		var f *flow
		f = newFlow(t, func(n int, _ sentPart) echoReply {
			if n == 1 {
				return unconfirmed(f.clock)
			}
			return ok("m2")
		}, on)
		f.r.p.opt.Expand.After = absent
		f.deliver(on, textAndPics("编的正文", pic()))
		return f.out.sent()
	}
	if s := run(func() *types.MediaResource { return bigPicture(t) }); len(s) != 2 {
		t.Fatalf("大图历史里没找到：按没送到转图重发：%d 帧", len(s))
	}
	if s := run(func() *types.MediaResource { return picture(t, 2) }); len(s) != 2 {
		t.Fatalf("小图历史里没找到：转图重发：%d 帧", len(s))
	}
}

// 大动图（pixiv 动图转的 GIF 常有几 MB）：没回声按没送到处理，动图没有可换的形式，直接退文字版；等回声的时间大图更久，小动图仍是 2 秒
func TestBigGIFNoEchoFallsToTextAfterLongerWait(t *testing.T) {
	t.Parallel()
	gif := func(size int) types.Part {
		m := types.MemoryMedia("g", "image/gif", 720, 405, bytes.Repeat([]byte{1}, size))
		return types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyText, Text: "动图"}, {Kind: types.BodyGIF, Media: m}}}
	}
	run := func(size int) (*flow, sendReport) {
		var f *flow
		f = newFlow(t, func(_ int, p sentPart) echoReply {
			if len(p.images) > 0 {
				return unconfirmed(f.clock)
			}
			return okSent("mt", f.clock)
		}, nil)
		return f, f.deliver(nil, gif(size))
	}
	big, rep := run(5 << 20)
	if len(big.out.sent()) != 2 || !rep.fallback || !rep.landed || rep.unsure {
		t.Fatalf("大动图没回声：退文字版，不是结果不明：%d 帧 %+v", len(big.out.sent()), rep)
	}
	if len(big.out.waits) != 1 || big.out.waits[0] <= 20*time.Second {
		t.Fatalf("大动图等回声要等久：%v", big.out.waits)
	}
	small, rep := run(10 << 10)
	if !rep.fallback || len(small.out.waits) != 1 || small.out.waits[0] != 2*time.Second {
		t.Fatalf("小动图照旧：2 秒、退文字版：%v %+v", small.out.waits, rep)
	}
}
