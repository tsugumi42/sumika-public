package settingsreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 没人写过说明的键留空，不再用模板糊一句。清单在 testdata/undescribed.txt，只许缩短：
// 新加的键要么写说明，要么不进这份清单（不进就红）；写了说明的键要从清单里删掉。
func TestUndescribedKeysOnlyShrink(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "undescribed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			listed[line] = true
		}
	}
	for _, e := range Entries() {
		switch {
		case e.Description == "" && !listed[e.Key]:
			t.Errorf("%s 没有说明，也不在 testdata/undescribed.txt 里：请给它写一句说明", e.Key)
		case e.Description != "" && listed[e.Key]:
			t.Errorf("%s 已经有说明了，请从 testdata/undescribed.txt 里删掉", e.Key)
		}
		delete(listed, e.Key)
	}
	for k := range listed {
		t.Errorf("testdata/undescribed.txt 里的 %s 不是登记的键", k)
	}
}
