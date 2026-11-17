package turn

import "sumika-native/internal/audience"

// 自检会话（audience.IsSelftest）里存下的每一条事件都带 selftest 标记：记忆整理丢掉带它的行，链接插件不取，
// 记忆插件不记。标记在写库这一处加，主人的话、插话、她的回复三处都经过这里，界面里手打进自检话题的话也一样被标。
// 提醒专用会话同样带这个标记（audience.NoMemory）：她在那里写的只是草稿，真正说出去的在主会话里。

// selftestTags 是 AppendInputEvent 用的标签；不是自检会话是 nil（摘要不变，旧行的比较不受影响）
func selftestTags(conversation string) []string {
	if audience.NoMemory(conversation) {
		return []string{audience.SelftestTag}
	}
	return nil
}

// selftestSegments 在原来的 segments（nil 或 map）上加 tags；不是自检会话原样返回
func selftestSegments(conversation string, segs any) any {
	if !audience.NoMemory(conversation) {
		return segs
	}
	m := map[string]any{}
	if old, ok := segs.(map[string]any); ok {
		for k, v := range old {
			m[k] = v
		}
	}
	m["tags"] = []string{audience.SelftestTag}
	return m
}
