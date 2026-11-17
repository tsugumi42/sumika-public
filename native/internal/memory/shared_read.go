package memory

// 合并快照只在角色或共用池版本改变时重建；每轮读取不复制整份记忆。
func (m *Memory) rawSnapshot() ([]Item, map[string]*Item, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.items, m.byRef, m.ver
}

func (m *Memory) combinedSnapshot() ([]Item, map[string]*Item, uint64) {
	items, by, ver := m.rawSnapshot()
	if m.opt.Shared == nil {
		return items, by, ver
	}
	shared, _, sv := m.opt.Shared.rawSnapshot()
	m.viewMu.Lock()
	defer m.viewMu.Unlock()
	if m.viewItems == nil || m.viewLocal != ver || m.viewShared != sv {
		m.viewItems = make([]Item, 0, len(items)+len(shared))
		m.viewItems = append(m.viewItems, items...)
		m.viewItems = append(m.viewItems, shared...)
		m.viewByRef = refIndex(m.viewItems)
		m.viewLocal, m.viewShared = ver, sv
		m.viewVersion++
	}
	return m.viewItems, m.viewByRef, m.viewVersion
}
