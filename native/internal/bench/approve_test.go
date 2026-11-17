package bench

import (
	"encoding/json"
	"os"
	"testing"
)

// 用例在 testdata/approve-cases.json（Bun 壳的 approve.test.ts（sumika-bun 仓库的 scripts/bench-t2/）读同一份，两边结论要一样）：
// B2、B3 批准版里被拦下的真实命令为主，工作目录里的批，指向外面的不批
func TestApproveInWorkdir(t *testing.T) {
	b, err := os.ReadFile("testdata/approve-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Workdir string `json:"workdir"`
		Cases   []struct {
			Tool, Subject string
			Dirs          []string
			Want          bool
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Cases) < 20 {
		t.Fatalf("用例读不了：%v，%d 条", err, len(f.Cases))
	}
	for _, c := range f.Cases {
		if got := ApproveInWorkdir(c.Tool, c.Subject, f.Workdir, c.Dirs); got != c.Want {
			t.Errorf("%s %q：%v，要 %v", c.Tool, c.Subject, got, c.Want)
		}
	}
}
