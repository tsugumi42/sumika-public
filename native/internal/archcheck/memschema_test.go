package archcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 记忆库的测试夹具只许放在 internal/memory/memtest（规格-记忆库从零建库 设计第 6 条、验收第 9 条）：
// 1. memtest 只给测试用，非测试代码不许 import —— 否则打包进常驻程序，还会把 testing 带进去；
// 2. 基础建表语句只有 internal/memory/shared.go 的 BaseSchema 一份。三份工具抄本 2026-11-16 已删，
//    以后谁再往工具或 .sql 里抄一份，这里红。
//
// 测试文件里的全量抄本改成调 memtest；故意裁剪列 / 故意旧形状的夹具不进扫描（机器分不出“故意”，靠复核看）。

const memtestPkg = "sumika-native/internal/memory/memtest"

// TestMemtestOnlyImportedByTests：go list 的 Imports 只算非测试文件的 import。
func TestMemtestOnlyImportedByTests(t *testing.T) {
	all := allPackages(t)
	if !all[memtestPkg] {
		t.Fatalf("模块里没有 %s：改名或搬家了就把 memtestPkg 一起改", memtestPkg)
	}
	for pkg, info := range moduleInfo(t) {
		if pkg == memtestPkg {
			continue
		}
		for _, imp := range info.Imports {
			if imp == memtestPkg {
				t.Errorf("%s 在非测试代码里 import 了 %s：这个包只给测试用（规格-记忆库从零建库 第 6 条）", pkg, memtestPkg)
			}
		}
	}
}

// memoryFactsTable 是“建 facts 表”的写法：CREATE TABLE facts / CREATE TABLE IF NOT EXISTS facts，不分大小写。
var memoryFactsTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?facts\b`)

// TestMemorySchemaHasOneSource 钉住基础建表语句只有一份：
// native/ 下非 _test.go 的 .go 文件和所有 .sql 文件（跳过冻结的 store/testdata/milestones/）里，
// 能匹配 memoryFactsTable 的只有 internal/memory/shared.go。
func TestMemorySchemaHasOneSource(t *testing.T) {
	root := moduleRoot(t)
	const allowed = "internal/memory/shared.go"
	var hits []string
	sawAllowed, scanned := false, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "internal/store/testdata/milestones" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".sql") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !memoryFactsTable.Match(b) {
			return nil
		}
		scanned++
		if rel == allowed {
			sawAllowed = true
			return nil
		}
		hits = append(hits, "native/"+rel)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 native/：%v", err)
	}
	if !sawAllowed {
		t.Fatalf("native/%s 里没有建 facts 表的语句：BaseSchema 改名或换写法了，把这条规则一起改，不能什么也没查就过", allowed)
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Errorf("基础建表语句只许在 native/%s 的 BaseSchema 一处，这些文件又抄了一份：\n%s", allowed, strings.Join(hits, "\n"))
	}
	if scanned == 0 {
		t.Fatal("一份建 facts 表的语句都没扫到：根目录不对或扫描器坏了")
	}
}
