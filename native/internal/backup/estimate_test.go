package backup

import (
	"strings"
	"testing"
)

// 带全部媒体时，预计大小要扣掉能从上一份好备份硬链接复用的部分：不扣的话第二天起每天都按全量算 2 倍，天天误报空间不够
func TestEstimateDeductsLinkableMedia(t *testing.T) {
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("b", 64)
	items := []item{
		{rel: "go/chat/qq.db", size: 1000, kind: kindDB},
		{rel: "config/sumika.yml", size: 100, kind: kindFile},
		{rel: "go/media/qq/aa/" + h1, size: 5000, kind: kindMedia},
		{rel: "go/media/qq/bb/" + h2, size: 7000, kind: kindMedia},
		{rel: "go/media/qq/cc/not-a-hash.bin", size: 300, kind: kindMedia},
	}
	prev := &linkFrom{files: map[string]FileEntry{
		"go/media/qq/aa/" + h1:          {Size: 5000}, // 同名同大小：能复用
		"go/media/qq/bb/" + h2:          {Size: 6999}, // 大小不一样：不能复用
		"go/media/qq/cc/not-a-hash.bin": {Size: 300},  // 文件名不是哈希：不复用
	}}
	if got := estimateBytes(items, MediaAll, nil); got != 1000+100+5000+7000+300 {
		t.Fatalf("没有上一份：全量：%d", got)
	}
	if got := estimateBytes(items, MediaAll, prev); got != 1000+100+7000+300 {
		t.Fatalf("能复用的那一个不占新空间：%d", got)
	}
	if got := estimateBytes(items, MediaStickers, prev); got != 1000+100 {
		t.Fatalf("stickers 档只算库和文件：%d", got)
	}
}

// 带全部媒体的第二份备份：空间检查按“新占的”算，不被已经共用的媒体拦下
func TestAllMediaSecondRunIsNotBlockedBySharedMedia(t *testing.T) {
	f := newFx(t, func(o *Options) { o.MinFreeBytes = 1 })
	big := strings.Repeat("m", 300_000)
	write(t, f.p.GoMediaDir()+"/qq/aa/"+hSticker, big)
	if _, err := f.svc.Run(t.Context(), Request{Reason: ReasonDaily, Media: MediaAll}); err != nil {
		t.Fatal(err)
	}
	// 剩余空间没法精确控制，所以直接看估算
	items, _, _, err := collect(t.Context(), f.data)
	if err != nil {
		t.Fatal(err)
	}
	b := &builder{s: f.svc, dir: f.dir()}
	b.prev, b.prevDone = b.linkSource(), true
	full := estimateBytes(items, MediaAll, nil)
	deducted := estimateBytes(items, MediaAll, b.prev)
	if deducted >= full || full-deducted < int64(len(big)) {
		t.Fatalf("上一份带过的媒体该被扣掉：全量 %d，扣完 %d", full, deducted)
	}
}
