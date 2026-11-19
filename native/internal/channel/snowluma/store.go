package snowluma

import (
	"errors"
	"fmt"
	"strings"
)

// Bun 后台的数据目录里，这两个目录下面的库归它管：
//   - shared/  ：events.db、trace.db、learning.db
//   - characters/<id>/：memory.db、memory-v2.db、卡片历史
//
// 两个进程同时写一个 SQLite 文件会互相锁、丢数据（规格-QQ渠道九点五、规格-渠道中间件第十六节 B"分库"）。
// Go 写自己的 <数据目录>/go/ 下面的库。
var bunDirs = []string{"shared", "characters"}

// CheckDBPath 是启动时的检查：Go 要打开的库如果落在 Bun 的目录里就拒绝启动，并说清楚原因。
// 装配时对每个平台库的路径（paths.ChatDB）和运行记录的路径（paths.GoTraceDB）各调一次：它们的路径都由 paths 给，
// 这里只把"路径归谁"这一条守死（规格-聊天记录按平台分库 第二节）。
// 只认路径的分隔符，不读文件、不碰真实的 data/。
func CheckDBPath(file string) error {
	if strings.TrimSpace(file) == "" {
		return errors.New("snowluma: 没有给库路径")
	}
	parts := strings.FieldsFunc(file, func(r rune) bool { return r == '/' || r == '\\' })
	// 最后一段是文件名，目录才认得
	for _, p := range parts[:max(0, len(parts)-1)] {
		for _, b := range bunDirs {
			if strings.EqualFold(p, b) {
				return fmt.Errorf("snowluma: %s 在 Bun 的数据目录（%s/）下，Bun 后台正在用那里的库，Go 不能写（两个进程不许写同一个库文件）；Go 用自己的 <数据目录>/go/chat/<平台>.db 和 <数据目录>/go/trace.db", file, b)
			}
		}
	}
	return nil
}
