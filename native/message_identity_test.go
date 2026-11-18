package main

import (
	"github.com/egoist/mygo/ui"
	"image"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
	"testing"
)

func TestMessageInitialsAndOwnerColor(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, local := range []bool{false, true} {
			key := api.ConversationKey{Platform: "qq", Scene: "private", Conversation: "qq:private:90001"}
			if local {
				key.Platform = "local"
				key.Conversation = "console:aoi"
			}
			if group {
				key.Scene = "group"
				if local {
					key.Conversation = "console:group:room"
				} else {
					key.Conversation = "qq:group:90001"
				}
			}
			v := view("identity", "human", "A", "编造正文", t0)
			v.Ref.Conversation = key
			if local {
				v.ActorKind, v.ActorID, v.Origin = "character", "aoi", "character"
			}
			m := msgFromView(key, v, charOf)
			m.who, m.md = "A", false
			a := chatApp([]*msg{m}, group)
			p := newProbes(a)
			var accent ui.Color
			tt := ui.NewTester(func(c *ui.Context) { p.ctx = c; accent = c.Theme().Accent; a.chatView(c) }, 900, 600)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.Frame()
			tt.Frame()
			row := p.box(t, "shell:identity:0")
			avatar := image.Rect(int(row.X+20), int(row.Y+row.H-29), int(row.X+41), int(row.Y+row.H-8))
			before := tt.Image()
			m.who = "D"
			tt.Frame()
			after := tt.Image()
			changed := 0
			for y := avatar.Min.Y; y < avatar.Max.Y; y++ {
				for x := avatar.Min.X; x < avatar.Max.X; x++ {
					if before.RGBAAt(x, y) != after.RGBAAt(x, y) {
						changed++
					}
				}
			}
			if changed < 5 {
				t.Fatalf("local=%v group=%v 缺图首字没有变化，changed=%d", local, group, changed)
			}
			owner := view("identity", "owner", "显示主人", "编造主人正文", t0)
			owner.Ref.Conversation = key
			m = msgFromView(key, owner, charOf)
			a.msgs = []*msg{m}
			for _, mediaOnly := range []bool{false, true} {
				if mediaOnly {
					picture := segFromDisplay(api.DisplaySegment{Kind: "image", Media: &api.MediaRef{Hash: "identity-photo"}, Width: 120, Height: 120})
					m.segs = []seg{picture}
					m.blocks = segview.BlocksOf(segmentViews(m.segs))
					a.imgs = newImageStore(t.Context(), a.post)
					bitmap := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 120, 120)))
					for _, tier := range thumbTiers {
						a.imgs.m[imgKey{"identity-photo", tier, false}] = &imgEntry{state: imgOK, bmp: bitmap}
					}
				}
				tt.Frame()
				if group {
					name, ok := tt.Find("显示主人")
					if !ok {
						t.Fatalf("local=%v 群聊没有主人名字", local)
					}
					frame := tt.Image()
					count := 0
					for y := int(name.Y); y < int(name.Y+name.H); y++ {
						for x := int(name.X); x < int(name.X+name.W); x++ {
							v := frame.RGBAAt(x, y)
							if v.R == accent.R && v.G == accent.G && v.B == accent.B {
								count++
							}
						}
					}
					if count < 2 {
						t.Fatalf("local=%v 主人名字没有强调色像素：%d", local, count)
					}
				} else if hasExact(tt, "显示主人") {
					t.Fatal("私聊多出名字行")
				}
			}
		}
	}
}
