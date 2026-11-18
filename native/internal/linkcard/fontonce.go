package linkcard

import (
	"hash/fnv"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"
)

// mygo 的 RegisterFont 把字体追加进一张进程级的表，只进不出；之后每个新建的离屏画布都要把表里的字体全部再解析一遍。
// 每个 Renderer 第一次画推特卡片都会注册一遍文渊圆体和 Maple（几十 MB），重建 Renderer（测试里每个用例一个、设置改字体）
// 就会越攒越多、越建越慢：linkcard 的测试一路跑下来涨到二十几 GB（2026-11-17 合并队列）。
// 同一份字体（字族名 + 内容）在一个进程里只交给 mygo 一次；注册失败不记，下次照常再试。
var uiFonts = onceRegistrar(ui.RegisterFont)

// onceRegistrar 包一层注册函数：同一字族、同一内容只调 inner 一次
func onceRegistrar(inner func([]byte, string) error) func([]byte, string) error {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(data []byte, family string) error {
		h := fnv.New64a()
		_, _ = h.Write(data)
		key := family + "\x00" + strconv.Itoa(len(data)) + "\x00" + strconv.FormatUint(h.Sum64(), 16)
		mu.Lock()
		defer mu.Unlock()
		if seen[key] {
			return nil
		}
		if err := inner(data, family); err != nil {
			return err
		}
		seen[key] = true
		return nil
	}
}
