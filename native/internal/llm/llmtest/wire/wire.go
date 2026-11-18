// Package wire 把假网关收到的原始请求体和录下的整份字节对拍，并在重录时写回。
// 两侧都是原样字节：不格式化、不解析、不加换行。重录要先在 intended.md 登记场景，
// 照 turn 的 INTENDED 惯例，没登记的不写。
package wire

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Check 逐字节比对这一场景的几份请求体。一份是 <场景>.body；几份是 <场景>-01.body、<场景>-02.body……
// dir 是这个包的 testdata/wire（测试的工作目录就是包的目录）。
func Check(t *testing.T, dir, scenario string, bodies ...[]byte) {
	t.Helper()
	if len(bodies) == 0 {
		t.Fatalf("%s：没有请求体可对拍", scenario)
	}
	if os.Getenv("WIRE_UPDATE") == "1" {
		if why := registration(dir, scenario); why == "" {
			t.Fatalf("重录 %s 之前先在 %s 登记一行：- %s：原因", scenario, filepath.Join(dir, "intended.md"), scenario)
		}
		for i, body := range bodies {
			path := filepath.Join(dir, fileName(scenario, i, len(bodies)))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for i, body := range bodies {
		path := filepath.Join(dir, fileName(scenario, i, len(bodies)))
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s 读不到：%v（首次录制：先在 %s 登记，再用 WIRE_UPDATE=1 跑一遍）", path, err, filepath.Join(dir, "intended.md"))
		}
		if !bytes.Equal(want, body) {
			t.Errorf("%s 和录下的字节不一样：\n%s", path, diff(want, body))
		}
	}
}

// fileName 是第 i 份（从 0 数）在共 n 份时的文件名
func fileName(scenario string, i, n int) string {
	if n == 1 {
		return scenario + ".body"
	}
	return fmt.Sprintf("%s-%02d.body", scenario, i+1)
}

// registration 是这个场景在 intended.md 里的登记行（没有是空）。认 "- 场景：原因" 和 "- 场景: 原因"
func registration(dir, scenario string) string {
	b, err := os.ReadFile(filepath.Join(dir, "intended.md"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		for _, sep := range []string{"：", ":"} {
			prefix := "- " + scenario + sep
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return ""
}

// diff 说第一处不同的字节位置，并打出两边前后各 80 个字节
func diff(want, got []byte) string {
	n := min(len(want), len(got))
	pos := -1
	for i := range n {
		if want[i] != got[i] {
			pos = i
			break
		}
	}
	if pos < 0 {
		if len(want) == len(got) {
			return "（两边相同）"
		}
		pos = n
	}
	lo := max(0, pos-80)
	return fmt.Sprintf("第一处不同在第 %d 字节（录下 %d 字节，这次 %d 字节）\n录下的：%q\n这次的：%q",
		pos, len(want), len(got), want[lo:min(len(want), pos+80)], got[lo:min(len(got), pos+80)])
}
