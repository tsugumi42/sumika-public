package linkplugin

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 核实“这一条有没有进群”的判定（ADR-333）：全是编的号、编的图
const (
	selfID  = "10001"
	otherID = "20002"
)

func md5Of(b string) string {
	h := md5.Sum([]byte(b))
	return hex.EncodeToString(h[:])
}

// img("a") 的字节是 "编的图字节a"
func probeOfImg(t *testing.T, id string) probe {
	t.Helper()
	pr, ok := probeOf(msg("", img(id)))
	if !ok || len(pr.images) != 1 || pr.images[0] != md5Of("编的图字节"+id) {
		t.Fatalf("特征取错了：%+v %v", pr, ok)
	}
	return pr
}

func historyMsg(id, from string, at time.Time, segs ...types.Seg) types.Inbound {
	return types.Inbound{Kind: types.InboundMessage, PlatformID: id, From: types.User{ID: from}, Time: at, Segs: segs}
}

func imageSeg(file string) types.Seg { return types.Seg{Kind: types.SegImage, File: file} }

var t0 = time.Unix(1_800_000_000, 0)

func TestJudgeArrivedByMD5(t *testing.T) {
	pr := probeOfImg(t, "a")
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{
		historyMsg("1", otherID, t0, imageSeg("x.jpg")),
		historyMsg("2", selfID, t0.Add(3*time.Second), imageSeg(md5Of("编的图字节a")+".jpg")),
	}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyArrived {
		t.Fatal("自己号发的、md5 对上的，应当算到了")
	}
}

func TestJudgeArrivedByShapeWhenIDOpaque(t *testing.T) {
	pr := probeOfImg(t, "a")
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(time.Second), imageSeg("opaque-file-id"))}}
	if k, why := judge(page, pr, selfID, t0, nil); k != verifyArrived || why == "" {
		t.Fatalf("标识读不出时凭形状和时间算到了：%v %s", k, why)
	}
}

func TestJudgeOtherImageIsNotOurs(t *testing.T) {
	pr := probeOfImg(t, "a")
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(time.Second), imageSeg(md5Of("别的图")+".jpg"))}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyUnknown {
		t.Fatal("自己号发的另一张同形状图（md5 读得出且不同）不算到了，但也不能断言没到")
	}
}

func TestJudgeAbsentOnlyWhenReachedLatest(t *testing.T) {
	pr := probeOfImg(t, "a")
	page := types.AfterPage{Messages: []types.Inbound{historyMsg("1", otherID, t0.Add(time.Second), types.Seg{Kind: types.SegText, Text: "别人的话"})}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyUnknown {
		t.Fatal("没翻到尽头，找不到不能说没到")
	}
	page.ReachedLatest = true
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyAbsent {
		t.Fatal("翻到了尽头还没有，才是没到")
	}
}

func TestJudgeIgnoresEarlierExcludedAndWrongShape(t *testing.T) {
	pr := probeOfImg(t, "a")
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{
		historyMsg("1", selfID, t0.Add(-120*time.Second), imageSeg("opaque")),           // 发送开始之前很久的
		historyMsg("2", selfID, t0.Add(time.Second), imageSeg("opaque")),                // 这次展开里已确认的别的段
		historyMsg("3", selfID, t0.Add(time.Second), imageSeg("o1"), imageSeg("o2")),    // 图数不对
		historyMsg("4", selfID, t0.Add(time.Second), types.Seg{Kind: types.SegForward}), // 形状不对
	}}
	if k, _ := judge(page, pr, selfID, t0, map[string]bool{"2": true}); k != verifyAbsent {
		t.Fatal("时间太早、已确认的别的段、形状不对的都不算，翻到尽头就是没到")
	}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyArrived {
		t.Fatal("不排除它时，它就是候选")
	}
}

func TestJudgeForwardAndTextShape(t *testing.T) {
	pr, ok := probeOf(fwd(node(img("a"))))
	if !ok || !pr.forward {
		t.Fatal("转发的特征")
	}
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("9", selfID, t0.Add(time.Second), types.Seg{Kind: types.SegForward})}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyArrived {
		t.Fatal("恰有一个转发段、没有图，算合并转发到了")
	}
	withText, _ := probeOf(msg("", txt("标题"), img("a")))
	page = types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("9", selfID, t0, types.Seg{Kind: types.SegText, Text: "别的字"}, imageSeg("o"))}}
	if k, _ := judge(page, withText, selfID, t0, nil); k != verifyAbsent {
		t.Fatal("文字不一样不是这一条")
	}
}

func TestJudgeClockSkewIsNotAbsent(t *testing.T) {
	pr := probeOfImg(t, "a")
	// 本机时钟比 QQ 快 5 秒：真到了的消息时间落在“发送开始”之前 5 秒，窗口放宽后仍认得出
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(-5*time.Second), imageSeg("opaque"))}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyArrived {
		t.Fatal("时钟偏快 5 秒时真到了的消息应认得出（旧窗口只留 2 秒，会判没到后重复发）")
	}
	// 偏得更多、落在窗口外：不当到了，但同形状的自己号消息也不能断言没到
	page = types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(-20*time.Second), imageSeg("opaque"))}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyUnknown {
		t.Fatal("窗口外的同形状自己号消息可能是时钟误差，应判查不了")
	}
}

func TestJudgeSameShapeButOtherIDIsUnknown(t *testing.T) {
	pr := probeOfImg(t, "a")
	// 图标识读得出却对不上：本身不算到了，但形状一致，不能断言没到
	page := types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(time.Second), imageSeg(md5Of("别的图")+".jpg"))}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyUnknown {
		t.Fatal("形状一致、标识对不上的自己号消息：判查不了，不判没到")
	}
	// 形状也不一致的（图数不同）才是明确的“不是这一条”
	page = types.AfterPage{ReachedLatest: true, Messages: []types.Inbound{historyMsg("2", selfID, t0.Add(time.Second), imageSeg("o1"), imageSeg("o2"))}}
	if k, _ := judge(page, pr, selfID, t0, nil); k != verifyAbsent {
		t.Fatal("形状不一致不构成疑点")
	}
}

func TestProbeOfNeedsBytes(t *testing.T) {
	if _, ok := probeOf(msg("", txt("纯文字"))); ok {
		t.Fatal("没有媒体取不了特征")
	}
	if _, ok := probeOf(msg("", types.BodySegment{Kind: types.BodyVideo})); ok {
		t.Fatal("视频没有字节，取不了特征")
	}
}
