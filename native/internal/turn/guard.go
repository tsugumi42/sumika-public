package turn

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
)

// 防卡死（daemon/util/tool-guard.ts）：同一个工具、同样的参数、得到同样的结果，连着出现第 3、5、8 次时，
// 在这次工具结果末尾追加一句提醒。只数连续的，中间夹了别的调用就从 1 数；todo_write 不算（既不数也不清零）；
// 主人说了新的话（新的一轮、插话送到）从头数
var nudgeAt = []int{3, 5, 8}

type repeatGuard struct {
	last  string
	count int
}

// observe 看一次执行过的工具调用（pi 的 afterToolCall：没找到的工具、参数不对、被拦下的不经过这里）；该提醒时返回追加的那句话
func (g *repeatGuard) observe(name string, args jsjson.Value, resultText string) string {
	if name == "todo_write" {
		return ""
	}
	// purpose 是她写给主人看的话，换个说法重试同一件事也算同一次
	rest := jsjson.Without(args, "purpose")
	sum := sha1.Sum([]byte(resultText))
	key := name + "\n" + jsjson.Stringify(jsjson.Canonical(rest)) + "\n" + hex.EncodeToString(sum[:])[:16]
	if key == g.last {
		g.count++
	} else {
		g.count = 1
	}
	g.last = key
	n := g.count
	if n == nudgeAt[0] {
		return fmt.Sprintf("（提醒：你已经连续 %d 次用完全一样的参数调用「%s」，得到的结果也一样。先看清上一次的结果；没做完就换个办法或换参数，别再原样重试。）", n, name)
	}
	if slices.Contains(nudgeAt, n) {
		return fmt.Sprintf("（提醒：「%s」已经连续 %d 次用同样的参数调用、结果相同，没有进展。参数：%s。不要再用这组参数调用它；换一种做法，或者证据已经够了就收尾，把卡在哪里告诉所有者。）", name, n, store.JSSlice(jsjson.Stringify(rest), 200))
	}
	return ""
}
