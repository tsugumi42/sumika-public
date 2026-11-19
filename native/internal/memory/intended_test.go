package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 有意和 Bun 不一样的地方登记在 testdata/intended.json（每条写 Bun 的行为、Go 的行为、出处；规格-记忆-v2 13.2.11、ADR-183 第 6 条）。
// 断言这些行为的测试用 intended(t, "名字") 引用登记项：改行为要同时改登记，登记里有的没人引用、引用的没登记，都报错

type intendedItem struct {
	Name, Bun, Go, Source string
}

func loadIntended(t *testing.T) []intendedItem {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "intended.json"))
	if err != nil {
		t.Fatal(err)
	}
	var items []intendedItem
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("intended.json 读不懂：%v", err)
	}
	return items
}

// intended 在测试里引用一条登记；登记里没有就失败
func intended(t *testing.T, name string) intendedItem {
	t.Helper()
	for _, it := range loadIntended(t) {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("intended.json 里没有登记 %q", name)
	return intendedItem{}
}

func TestIntendedRegistry(t *testing.T) {
	t.Parallel()
	items := loadIntended(t)
	seen := map[string]bool{}
	for _, it := range items {
		if it.Name == "" || it.Bun == "" || it.Go == "" || it.Source == "" {
			t.Errorf("登记项 %q 缺字段（要写 name、bun、go、source）", it.Name)
		}
		if seen[it.Name] {
			t.Errorf("登记项 %q 重复", it.Name)
		}
		seen[it.Name] = true
	}
	// 每一条都要有测试引用它
	files, _ := filepath.Glob("*_test.go")
	var src strings.Builder
	for _, f := range files {
		if f == "intended_test.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		src.Write(b)
	}
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`intended\(t, "([^"]+)"\)`).FindAllStringSubmatch(src.String(), -1) {
		used[m[1]] = true
		if !seen[m[1]] {
			t.Errorf("测试引用了没登记的 %q", m[1])
		}
	}
	for name := range seen {
		if !used[name] {
			t.Errorf("登记项 %q 没有测试引用它：删掉登记，或者补测试", name)
		}
	}
}
