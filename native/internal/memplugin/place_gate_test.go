package memplugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/memory"
)

// 场合过滤的五条入口（规格-记忆写入上线 W5 验收 2）：通知、启动扫描、整理按钮和归档（request）、压缩前（beforeCompact）、立游标（ensureCursor），
// 再加直接调 SummarizeBatch。会话、号码、名字都是编的；预期表手写，不调被测的 PlaceMayWrite

type gateScene struct {
	name, conv string
	peer       bool // 会话里的话是对方（群友）说的，不是主人说的
	// 各份放行名单下该不该整理：默认（nil）、只开 platform-private、空名单、五个全开
	want [4]bool
}

var gateScenes = []gateScene{
	{"本地私聊", "console:suzu", false, [4]bool{false, false, false, true}},
	{"本地群聊", "console:group:g1", false, [4]bool{false, false, false, true}},
	{"QQ 主人的私聊", "qq:private:20001:60001", false, [4]bool{true, true, false, true}},
	{"QQ 别人的私聊", "qq:private:20001:60002", true, [4]bool{false, true, false, true}},
	{"QQ 群", "qq:group:5001001", false, [4]bool{false, false, false, true}},
	{"陌生渠道", "telegram:private:1:2", false, [4]bool{false, false, false, false}},
	{"形状错的私聊", "qq:private:7001", false, [4]bool{false, false, false, false}},
	{"形状错的群", "qq:group:abc", false, [4]bool{false, false, false, false}},
}

var gatePlaces = []struct {
	name   string
	places []string
}{
	{"默认名单", nil},
	{"只开 platform-private", []string{memory.WritePlacePlatformPrivate}},
	{"空名单", []string{}},
	{"五个全开", memory.AllWritePlaces()},
}

// gateRig 造一个台子：角色 suzu，放行名单是 places，阈值 1 条、扫描 suzu
func gateRig(t *testing.T, places []string) (*rig, *writable) {
	t.Helper()
	r := newRigExtra(t, rigExtras{memOpts: func(o *memory.Options) { o.WritePlaces = places }}, func(o *Options) {
		o.Threshold = 1
		o.Characters = func() []string { return []string{"suzu"} }
	}, nil)
	w := r.mems["suzu"]
	return r, w
}

func (s gateScene) add(w *writable, n int) {
	for i := 0; i < n; i++ {
		w.ev.add(s.conv, "编造的待整理消息", func(e *memory.Event) {
			if s.peer {
				e.ActorKind, e.ActorID, e.Origin = "human", "qq:60002", "peer"
			}
		})
	}
}

// obs 是一格的观察：模型调用几次、游标有没有行/在哪、读了几次事件
type obs struct {
	gen     int
	hasCur  bool
	cursor  int64
	evReads int
}

func observe(w *writable, conv string) obs {
	seq, ok := w.cursor(conv)
	return obs{gen: w.gen.count(), hasCur: ok, cursor: seq, evReads: w.ev.readCount()}
}

func TestPlaceGate(t *testing.T) {
	t.Parallel()
	entries := []struct {
		name string
		run  func(t *testing.T, r *rig, w *writable, s gateScene) (processed bool)
	}{
		{"通知（新会话）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			r.enable()
			r.idle() // 等启动扫描扫完（库里还没有这个会话），再来新会话
			s.add(w, 3)
			r.notify("suzu", s.conv, 1)
			o := observe(w, s.conv)
			return o.gen == 1 && o.hasCur && o.cursor > 0
		}},
		{"整理按钮（request，新会话）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			r.enable()
			r.idle() // 等启动扫描扫完（库里还没有这个会话），再来新会话
			s.add(w, 3)
			r.p.Summarize("suzu", s.conv, true)
			r.idle()
			o := observe(w, s.conv)
			return o.gen == 1 && o.hasCur && o.cursor > 0
		}},
		{"压缩前（beforeCompact，新会话）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			r.enable()
			r.idle() // 等启动扫描扫完（库里还没有这个会话），再来新会话
			s.add(w, 3)
			upTo, limited := r.compact("suzu", s.conv, time.Minute)
			o := observe(w, s.conv)
			if !(o.gen == 1 && o.hasCur && o.cursor > 0) {
				if upTo != 0 || limited {
					t.Errorf("不放行的会话压缩钩子该交回 (0, false)，得到 (%d, %v)", upTo, limited)
				}
				return false
			}
			if upTo != o.cursor || !limited {
				t.Errorf("放行的会话交回 (%d, %v)，游标 %d", upTo, limited, o.cursor)
			}
			return true
		}},
		{"立游标（ensureCursor，新会话）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			r.enable()
			r.idle() // 等启动扫描扫完（库里还没有这个会话），再来新会话
			s.add(w, 3)
			got := r.lf.s.ensureCursor(w.m, skey{"suzu", s.conv})
			_, has := w.cursor(s.conv)
			if got != has {
				t.Errorf("ensureCursor 返回 %v，但游标行 %v", got, has)
			}
			return got
		}},
		{"启动扫描（没有游标的老会话）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			s.add(w, 3)
			r.enable()
			r.idle()
			seq, has := w.cursor(s.conv)
			if has && seq != 3 {
				t.Errorf("启动扫描立的游标该是最新的 3（不回填），得到 %d", seq)
			}
			if w.gen.count() != 0 {
				t.Errorf("没有游标的老会话不整理，模型被调了 %d 次", w.gen.count())
			}
			return has
		}},
		{"启动扫描（导入搬来的旧游标，后面还有新的）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			s.add(w, 3)
			w.initCursor(t, s.conv, 0)
			r.enable()
			r.idle()
			o := observe(w, s.conv)
			if !o.hasCur || (o.gen == 0) != (o.cursor == 0) {
				t.Errorf("旧游标的会话：模型 %d 次，游标 %d（有行 %v）", o.gen, o.cursor, o.hasCur)
			}
			return o.gen == 1 && o.cursor > 0
		}},
		{"直接调 SummarizeBatch（旧游标）", func(t *testing.T, r *rig, w *writable, s gateScene) bool {
			s.add(w, 3)
			w.initCursor(t, s.conv, 0)
			res, err := w.m.SummarizeBatch(context.Background(), s.conv, true)
			if err != nil {
				t.Fatal(err)
			}
			o := observe(w, s.conv)
			if (res.Skipped == memory.SkipPlace) == (o.gen == 1) {
				t.Errorf("结局 %q 和模型调用 %d 次对不上", res.Skipped, o.gen)
			}
			if res.Skipped == memory.SkipPlace && o.cursor != 0 {
				t.Errorf("SkipPlace 以后游标动了：%d", o.cursor)
			}
			return o.gen == 1 && o.cursor > 0
		}},
	}
	for _, e := range entries {
		for _, s := range gateScenes {
			for pi, p := range gatePlaces {
				t.Run(e.name+"/"+s.name+"/"+p.name, func(t *testing.T) {
					t.Parallel()
					r, w := gateRig(t, p.places)
					got := e.run(t, r, w, s)
					if want := s.want[pi]; got != want {
						t.Fatalf("整理了 %v，应该是 %v（模型 %d 次，事件读了 %d 次）", got, want, w.gen.count(), w.ev.readCount())
					}
					if !got && (strings.HasPrefix(e.name, "通知") || strings.HasPrefix(e.name, "整理按钮")) {
						// memoryFor 提前挡：会话级就不放行的，连排程的槽位、计时都不建。只有"QQ 别人的私聊 + 默认名单"要读了事件才知道，槽位是有的
						r.lf.s.mu.Lock()
						_, slot := r.lf.s.slots[skey{"suzu", s.conv}]
						r.lf.s.mu.Unlock()
						if dynamic := s.name == "QQ 别人的私聊" && pi == 0; slot == !dynamic {
							t.Errorf("排程槽位 %v：会话级不放行的不该有，要读事件才知道的该有", slot)
						}
					}
					if !got {
						// 不放行的：没立游标、没调模型、没写任何东西
						if _, has := w.cursor(s.conv); has && e.name != "启动扫描（导入搬来的旧游标，后面还有新的）" && e.name != "直接调 SummarizeBatch（旧游标）" {
							t.Errorf("不放行的会话立了游标")
						}
						if w.gen.count() != 0 {
							t.Errorf("不放行的会话调了 %d 次模型", w.gen.count())
						}
						if n := w.rows(t, "facts") + w.rows(t, "episodes") + w.rows(t, "memory_jobs"); n != 0 {
							t.Errorf("不放行的会话往库里写了 %d 行", n)
						}
						if seq, has := w.cursor(s.conv); has && seq != 0 {
							t.Errorf("不放行的会话游标挪到了 %d", seq)
						}
					}
				})
			}
		}
	}
}
