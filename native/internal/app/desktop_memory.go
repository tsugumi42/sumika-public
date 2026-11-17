package app

// 桌面记忆页的读服务装配（规格-记忆页 4.1）：每个角色一个只读视图（库路径、确认三项配置、隔离线、事件来源），
// 名字取自现有的名册（Bun 事件库的 qq_people，和记忆的别名表同一份）。只读：不碰写入侧，不改任何配置。
// 认人的统一入口（ADR-198 第 2 条）做好以后，把 peopleIndex 换成它。

import (
	"os"
	"strings"
	"sync"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
	"sumika-native/internal/memview"
	"sumika-native/internal/memwrite"
	"sumika-native/internal/store/memsource"
)

// peopleIndex 是名册的内存索引：第一次用到才读（archive 模式没有走过 openMemories，名册是空的）
type peopleIndex struct {
	once   sync.Once
	people []memory.Person
	byID   map[string]string
}

func (a *App) peopleOnce(ix *peopleIndex) {
	ix.once.Do(func() {
		list := a.people.People
		if len(list) == 0 {
			if p, err := a.readPeople(); err == nil {
				list = p.People
			}
		}
		ix.people, ix.byID = list, make(map[string]string, len(list))
		for _, p := range list {
			if len(p.Names) > 0 {
				ix.byID[p.ID] = p.Names[0]
			}
		}
	})
}

// autoConfirming：这个角色的记忆这会儿开着写入和自动提升（memwrite 决定之后的实际状态）。没挂记忆（只看历史、没有库）就是否
func (a *App) autoConfirming(id string) bool {
	a.memMu.Lock()
	m := a.memories[id]
	a.memMu.Unlock()
	return m != nil && m.AutoConfirming()
}

func (a *App) memoryService(chars []api.Result[api.CharacterView]) *memview.Service {
	w := a.cfg.Memory.Write
	var list []memview.Character
	for _, r := range chars {
		id := r.Value.ID
		if id == "" || memory.ValidCharacterID(id) != nil {
			continue // 角色目录读不到时那一条没有 id
		}
		name := r.Value.Name
		if name == "" {
			name = id
		}
		db := a.paths.MemoryDB(id)
		if _, err := os.Stat(db); err != nil {
			db = ""
		}
		c := memview.Character{ID: id, Name: name, DB: db, Browse: memory.BrowserOptions{
			ConfirmOccasions: w.ConfirmOccasions, ConfirmKinds: w.ConfirmKinds, QuarantineSince: memwrite.QuarantineSince(w),
			SharedDB: a.paths.OwnerMemoryDB(),
			// 自动提升真不真发生，要问写入侧现在的实际状态：配置里整理和 autoConfirm 都写着开，
			// 前提检查没过 / 整理退成只读 / 只看历史时都不会提升，页面不能照配置说"攒够就会提升"
			AutoNow: func() bool { return a.autoConfirming(id) }, Now: a.now,
		}}
		if a.set != nil {
			if m, err := memsource.NewMulti(a.set, id); err == nil {
				c.Events = m
			}
		}
		list = append(list, c)
	}
	ix := &peopleIndex{}
	return memview.New(list, memview.Options{
		Now: a.now,
		Names: func(kind, id string) string {
			if kind != "person" {
				return ""
			}
			a.peopleOnce(ix)
			return ix.byID[id]
		},
		ResolvePerson: func(term string) []string {
			term = strings.ToLower(strings.TrimSpace(term))
			if len([]rune(term)) < 2 {
				return nil
			}
			a.peopleOnce(ix)
			var out []string
			for _, p := range ix.people {
				for _, n := range p.Names {
					if strings.Contains(strings.ToLower(n), term) {
						out = append(out, p.ID)
						break
					}
				}
				if len(out) >= 50 {
					break
				}
			}
			return out
		},
	})
}
