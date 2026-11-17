package chatui

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// mygo 的 RegisterFont 追加进进程级的表、只进不出，之后每个新 Tester 都把表里的字体全部重新解析一遍；
// 截图测试每个都注册一遍文渊圆体，整包跑下来涨到十几 GB（2026-11-17）。同一字族、同一文件一个进程只注册一次
var testFonts struct {
	mu   sync.Mutex
	done map[string]error
}

func registerTestFont(data []byte, family string) error {
	key := family + "\x00" + string(data[:min(len(data), 64)]) + "\x00" + strconv.Itoa(len(data))
	testFonts.mu.Lock()
	defer testFonts.mu.Unlock()
	if testFonts.done == nil {
		testFonts.done = map[string]error{}
	}
	if err, ok := testFonts.done[key]; ok {
		return err
	}
	err := ui.RegisterFont(data, family)
	testFonts.done[key] = err
	return err
}

// testTinyFont 是仓库里的测试子集（466 KB，ASCII 加“你好”，字形和 tnum 等特性取自完整文渊圆体）。
// 只测布局、尺寸、找控件的用例用它：每建一个 Tester 都会把注册过的字体整份复制进文字引擎，
// 完整字体（几十 MB）会把整包内存推到几个 GB（2026-11-17）。
const testTinyFont = "SumikaTestRounded"

func registerTinyTestFont(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", "WenYuanTiny.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registerTestFont(data, testTinyFont); err != nil {
		t.Fatal(err)
	}
	return testTinyFont
}
