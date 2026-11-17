package snowluma

import (
	"strings"
	"testing"
)

func TestCheckDBPath(t *testing.T) {
	ok := []string{
		`D:\data\go\chat\local.db`,
		`D:\data\go\chat\qq.db`,
		`/data/go/chat/telegram.db`,
		`D:\data\go\trace.db`,
		`D:\data\go\channel.db`,
		`D:\tmp\随便\events.db`,
	}
	for _, f := range ok {
		if err := CheckDBPath(f); err != nil {
			t.Errorf("%s 应当可以，却被拒：%v", f, err)
		}
	}

	bad := []string{
		`D:\data\shared\events.db`,       // Bun 的 shared
		`D:\data\shared\trace.db`,        // Bun 的 shared
		`/data/shared/learning.db`,       // 同一个目录的正斜杠写法
		`D:\data\characters\甲\memory.db`, // Bun 的角色库
		`/data/characters/乙/memory-v2.db`,
		`D:\DATA\SHARED\events.db`,         // Windows 上大小写不是两回事
		`D:\data\Characters\丙\card\1.json`, // 角色目录下面的一切
	}
	for _, f := range bad {
		if err := CheckDBPath(f); err == nil {
			t.Errorf("%s 是 Bun 的库，应当拒绝", f)
		} else if !strings.Contains(err.Error(), "Bun") || !strings.Contains(err.Error(), "不能写") {
			t.Errorf("%s 的拒绝理由没说清楚：%v", f, err)
		}
	}

	if err := CheckDBPath(""); err == nil {
		t.Error("空路径应当拒绝")
	}
}
