// Package catalog 是组件例子和登记表（规格-设计令牌 第八节）：每个例子是一个 Demo 函数，
// 在 register.go 里用 Add 登记；画廊程序 cmd/sumika-gallery 和测试都从 All 取。
//
// 例子只调用正式的组件代码（winshell.Kit 的方法、chatui 的渲染函数），不自己画长得像的东西；
// 尺寸、颜色一律取 Kit 里的令牌。包的层次：design < chatui < winshell < catalog < cmd/*。
package catalog

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// Entry 是登记表里的一条：一个组件族的例子。
type Entry struct {
	ID     string // 稳定的短名，英文小写加连字符，做控件键的前缀
	Family string // 组件族，画廊左边列表的分组名
	Name   string // 列表里显示的名字
	Blurb  string // 一句话：这一族里有什么、试一试能做什么
	// Demo 在舞台里画这一族：上面是状态排，下面是试一试。画廊每帧调用它，状态放在 env 的 Store 里。
	Demo func(c *ui.Context, env Env)
}

var (
	mu      sync.Mutex
	entries []Entry
)

// Add 登记一条。ID 重复、缺字段、没有 Demo 都直接 panic（编程错误，不吞）。
func Add(e Entry) {
	if e.ID == "" || e.Family == "" || e.Name == "" || e.Blurb == "" || e.Demo == nil {
		panic(fmt.Sprintf("catalog: 条目 %q 需要 ID、Family、Name、Blurb 和 Demo", e.ID))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, o := range entries {
		if o.ID == e.ID {
			panic(fmt.Sprintf("catalog: 条目 %q 重复登记", e.ID))
		}
	}
	entries = append(entries, e)
}

// All 按登记顺序返回全部条目（拷贝）。
func All() []Entry {
	mu.Lock()
	defer mu.Unlock()
	return append([]Entry(nil), entries...)
}

// Find 按 ID 取一条。
func Find(id string) (Entry, bool) {
	for _, e := range All() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Store 是例子的状态仓：计数、输入、审批进度都放这里，不放包级变量，所以测试和画廊各有各的。
// 画廊切换条目时不清它，切回来看到的还是刚才点过的样子。
type Store struct {
	vals map[string]any
	open map[string]*bool
	MD   *chatui.MD
	// Look 是聊天气泡材质的当前选择：聊天宿主（Env.ChatHost）按它决定气泡是实色还是玻璃，所有聊天类例子共用。
	Look BubbleLook
}

// NewStore 建一个空仓。
func NewStore() *Store {
	return &Store{vals: map[string]any{}, open: map[string]*bool{}, MD: chatui.NewMD(), Look: BubbleLook{Custom: bubbleAlphaCustom, Bar: true}}
}

// Reset 把一个条目的状态清回初始：值、展开状态都删掉，下次取用时按各自的初始值重建（画廊的“重放”用）。
func (s *Store) Reset(id string) {
	prefix := id + "/"
	for k := range s.vals {
		if strings.HasPrefix(k, prefix) {
			delete(s.vals, k)
		}
	}
	for k := range s.open {
		if strings.HasPrefix(k, prefix) {
			delete(s.open, k)
		}
	}
}

// Env 是画廊传给每个例子的东西。
type Env struct {
	Kit   *winshell.Kit
	Store *Store
	ID    string  // 当前条目的 ID
	Mono  string  // 等宽字体家族；空用系统等宽
	Room  float32 // 舞台可用宽度（窗口宽减去列表和内距），聊天列据此定宽
	// Replay 是“重放”按了几次：状态重置之后，靠它让只在第一次出现时播的进场（错开淡入）也重播。平时不用管。
	Replay int
}

// Key 给控件取全局唯一的键：Kit 里的分段、加减框、下拉都按字符串键记状态，不同条目、不同位置不能撞。
func (e Env) Key(name string) string { return e.ID + "/" + name }

// Local 取（第一次用时建）当前条目名下的一份状态。状态跨帧保留，换条目再切回来也在。
func Local[T any](e Env, name string, init func() T) *T {
	k := e.Key(name)
	if v, ok := e.Store.vals[k]; ok {
		return v.(*T)
	}
	p := new(T)
	if init != nil {
		*p = init()
	}
	e.Store.vals[k] = p
	return p
}

// Open 取一个展开状态（对话框、过程卡片）；同一个名字永远是同一个指针。
func (e Env) Open(name string) *bool {
	k := e.Key(name)
	if p := e.Store.open[k]; p != nil {
		return p
	}
	p := new(bool)
	e.Store.open[k] = p
	return p
}
