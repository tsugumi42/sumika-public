package memplugin

// 参考信息块里的记忆引用，走真的记忆和真的内核（规格-提示词前缀稳定 7.3）：QQ 群里召回了记忆、她回了话、交回以后，
// 块进了库；之后记忆变了（删、改写、改范围、过期、隔离、记忆库没了），重建时引用它的那一块整块不插，别的照旧。
// 没变的对照组：重建出来的请求以上一次请求为字节前缀。记忆是临时目录里编的库，群号、事实都是编的。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// blockWorld 是一个群里问了一轮、记忆块存了库的世界；change 摆布之后的记忆，再重建、再问一轮
type blockWorld struct {
	t    *testing.T
	k    *kernel
	file string
	cur  atomic.Pointer[memory.Memory]
}

func newBlockWorld(t *testing.T) *blockWorld {
	t.Helper()
	w := &blockWorld{t: t, k: newKernel(t)}
	w.file = bunDB(t, fixedNow, standardFacts())
	w.cur.Store(w.open(memory.Options{}))
	p := New(Options{Memories: func(string) *memory.Memory { return w.cur.Load() }, Logf: w.k.logs.logf, Now: func() time.Time { return fixedNow }})
	if err := w.k.hub.Use(p); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *blockWorld) open(opt memory.Options) *memory.Memory {
	w.t.Helper()
	m, err := memory.Open(context.Background(), w.file, nil, nil, opt)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { m.Close() })
	return m
}

// sql 直接改记忆库文件（模拟记忆写入以后条目表换新的结果）
func (w *blockWorld) sql(q string, args ...any) {
	w.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(w.file))
	if err != nil {
		w.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		w.t.Fatal(err)
	}
}

// ask 是主人在群里问了一句，等她回完、交回；返回这一次请求的下标
func (w *blockWorld) ask(text string) int {
	w.t.Helper()
	k := w.k
	seq, err := k.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupA, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{character})
	if err != nil {
		w.t.Fatal(err)
	}
	o := <-k.hub.Respond(turn.Input{CharacterID: character, Conversation: groupA, Seqs: []int64{seq}, Text: text, Audience: ownerInA, Addressed: true, Scope: memory.ScopeKey(ownerInA)})
	if o.Err != nil {
		w.t.Fatal(o.Err)
	}
	var lines []turn.Committed
	for _, s := range o.Result.Said {
		seq, err := k.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupA, ActorKind: "character", ActorID: character, Origin: "character", Kind: "message", Text: s.Text, CreatedAt: time.Now().UnixMilli()}, []string{character})
		if err != nil {
			w.t.Fatal(err)
		}
		lines = append(lines, turn.Committed{Step: s.Step, Seq: seq, Text: s.Text})
	}
	if err := k.hub.Commit(character, groupA, o.Result.TurnID, lines); err != nil {
		w.t.Fatal(err)
	}
	return k.requests() - 1
}

func TestBlockMemoryChangesSkipOnRebuild(t *testing.T) {
	t.Parallel()
	past := fixedNow.Add(-time.Hour).UnixMilli()
	cases := []struct {
		name   string
		change func(w *blockWorld)
		keep   bool // 对照组：记忆没变，块要插回
	}{
		{"记忆没变", func(w *blockWorld) { w.cur.Store(w.open(memory.Options{})) }, true},
		{"记忆被删", func(w *blockWorld) {
			w.sql(`UPDATE facts SET status = 'revoked' WHERE id = '1'`)
			w.cur.Store(w.open(memory.Options{}))
		}, false},
		{"记忆被改写", func(w *blockWorld) {
			w.sql(`UPDATE facts SET text = ? WHERE id = '1'`, "主人改学贝斯了")
			w.cur.Store(w.open(memory.Options{}))
		}, false},
		{"记忆改了范围", func(w *blockWorld) {
			w.sql(`UPDATE facts SET scope = 'public' WHERE id = '1'`)
			w.cur.Store(w.open(memory.Options{}))
		}, false},
		{"记忆过期", func(w *blockWorld) {
			w.sql(`UPDATE facts SET valid_to = ? WHERE id = '1'`, past)
			w.cur.Store(w.open(memory.Options{}))
		}, false},
		{"记忆被隔离", func(w *blockWorld) {
			w.cur.Store(w.open(memory.Options{QuarantineSince: fixedNow.Add(-time.Hour).UnixMilli()}))
		}, false},
		{"这个角色没有记忆库", func(w *blockWorld) { w.cur.Store(nil) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newBlockWorld(t)
			i0 := w.ask("吉他练得怎么样了")
			r0 := w.k.request(i0)
			if n, _ := inMessages(r0, textGuitar); n != 1 {
				t.Fatalf("第一轮该召回到「%s」：%d", textGuitar, n)
			}
			c.change(w)
			w.k.hub.RestartIn(character, groupA) // 实例放掉，下一轮从库里重建
			i1 := w.ask("今天天气不错")
			r1 := w.k.request(i1)
			n, _ := inMessages(r1, textGuitar)
			switch {
			case c.keep && n != 1:
				t.Fatalf("记忆没变，块该插回来（应在 1 条消息里，得到 %d）", n)
			case !c.keep && n != 0:
				t.Fatalf("记忆变了，引用它的块不该插回（得到 %d 条消息含「%s」）", n, textGuitar)
			}
			if c.keep && !isPrefix(r0, r1) {
				t.Fatal("记忆没变：重建以后的请求该以上一次请求为前缀")
			}
			if !c.keep && strings.Contains(w.k.logs.String(), "panic") {
				t.Fatal("不该出错")
			}
		})
	}
}

// 记忆写入路径上的清理：Hub.PurgeRefBlocks 删掉引用了这些记忆的块（记忆插件的 OnChanged 接到它），别的块不动
func TestBlockPurgedOnMemoryChange(t *testing.T) {
	t.Parallel()
	w := newBlockWorld(t)
	w.ask("吉他练得怎么样了")
	count := func() int {
		var n int
		if err := w.k.st.Events.QueryRow(`SELECT count(*) FROM ref_blocks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	if before == 0 {
		t.Fatal("第一轮该存下块")
	}
	w.k.hub.PurgeRefBlocks(character, []string{"f:没引用它"})
	if count() != before {
		t.Fatal("没有块引用它，不该删")
	}
	w.k.hub.PurgeRefBlocks("别的角色", []string{"f:1"})
	if count() != before {
		t.Fatal("别的角色的清理不该动这个角色的块")
	}
	w.k.hub.PurgeRefBlocks(character, []string{"f:1"})
	if count() != 0 {
		t.Fatalf("引用了 f:1 的块该删掉：%d", count())
	}
}

// 记忆在群里反复重建（说话人换来换去）：固定要点和命中不能随重建次数越攒越多
func TestBlockMemoryNotDuplicatedAcrossRebuilds(t *testing.T) {
	t.Parallel()
	w := newBlockWorld(t)
	var last int
	for i := 0; i < 5; i++ {
		last = w.ask("吉他练得怎么样了")
		w.k.hub.RestartIn(character, groupA) // 说话人换了：实例放掉，下一轮从库里重建
	}
	last = w.ask("再说说吉他")
	r := w.k.request(last)
	for _, sub := range []string{pinnedPublic, pinnedOwner, textGuitar} {
		if n, _ := inMessages(r, sub); n != 1 {
			t.Errorf("重建了 5 次以后，「%s」应只在 1 条消息里，实际 %d 条", sub, n)
		}
	}
}
