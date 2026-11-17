package main

import (
	"image"
	"image/color"
	"testing"
	"time"

	"sumika-native/internal/api"
)

// 表情回应上的小头像：QQ 上的人有头像键；角色和主人（这份数据里没有号）画首字。
func TestReactorAvatarKeys(t *testing.T) {
	t.Parallel()
	got := chatReactions("qq", []api.ReactionView{
		{ID: "1", Ref: "14", ActorKind: "human", ActorID: "qq:90051", DisplayName: "虚构甲"},
		{ID: "2", Ref: "14", ActorKind: "character", ActorID: "lingyin", DisplayName: "铃音"},
	})
	if len(got) != 1 || len(got[0].By) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].By[0].Avatar != avatarKey("user", "90051") {
		t.Fatalf("群友的头像键：%q", got[0].By[0].Avatar)
	}
	if got[0].By[1].Avatar != "" {
		t.Fatalf("角色不该有 QQ 头像键：%q", got[0].By[1].Avatar)
	}
	if local := chatReactions("local", []api.ReactionView{{ID: "1", Ref: "14", ActorKind: "human", ActorID: "qq:90051"}}); local[0].By[0].Avatar != "" {
		t.Fatalf("本地会话不走 QQ 头像：%q", local[0].By[0].Avatar)
	}
}

// 动的小黄脸循环播放：过一遍以后还在换帧；减少动态效果时停在第一帧。
func TestFaceAnimationLoops(t *testing.T) {
	t.Parallel()
	one, two := solid(8, 8, color.NRGBA{255, 0, 0, 255}), solid(8, 8, color.NRGBA{0, 0, 255, 255})
	data := apngSample(t, one, []image.Image{one, two}, []int{100, 100}, 1)
	bmp, an := decodeFaceAnimated(data)
	if bmp == nil || an == nil || len(an.frames) < 2 {
		t.Fatalf("没解出帧：%v %v", bmp, an)
	}
	if an.plays != 0 {
		t.Fatalf("该无限循环，plays=%d", an.plays)
	}
	f := newFaceStore(t.Context(), func(fn func()) { fn() })
	f.imgs["1"] = &faceEntry{state: faceOK, bmp: bmp, anim: an}
	an.startedAt = time.Now().Add(-time.Duration(an.total*5+an.delays[0]+1) * time.Millisecond)
	f.animate("1", false)
	if an.cur == 0 {
		t.Fatal("放过五遍以后还该接着换帧")
	}
	f.animate("1", true)
	if an.cur != 0 {
		t.Fatalf("减少动态效果该停在第一帧，现在第 %d 帧", an.cur)
	}
	if f.imgs["1"].anim == nil {
		t.Fatal("动图的帧不该丢")
	}
}

// 静态脸不走动画。
func TestStaticFaceHasNoAnimation(t *testing.T) {
	t.Parallel()
	if bmp, an := decodeFaceAnimated(facePNG(8, 8, red)); bmp == nil || an != nil {
		t.Fatalf("静态脸：%v %v", bmp, an)
	}
}
