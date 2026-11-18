package tokenizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 全部码点的 NFD、NFKD 形式对 Python 的分词（复核补的）：类别表（categories.json）漏了一类字符时，这一批会量出来，
// 不靠手工拼范围。原来的 T5 漏了 Mc 里的 Other_Grapheme_Extend（孟加拉文 U+09C7 U+09BE 之类），日常的样本里一条都没有，
// 是对着全部码点才看出来的。
//
// 参考数据几 MB，不进 git：用 tools/local-models/parity_ref.py sweep 在能 import server.py 的 Python 里导（命令在它的文件头），
// --out 放临时目录，再用 SUMIKA_TEST_SWEEP_DIR 指到那里跑（还要 SUMIKA_TEST_MODELS_ROOT）。没设就跳过。
// 文字和 id 都在文件里，Go 不自己算 NFD、NFKD（Python 的 Unicode 表是 15.0，Go 的更新，同一个码点的分解可能不同）。
// 要求：每个和 Python 不一致的文本都归得到类别，并且去掉类别字符以后 Go 和 Python 完全一致；最后记一行各类别的计数。
type sweepRef struct {
	Unicode   string       `json:"unicode"`
	Texts     []string     `json:"texts"`
	IDs       [][]int      `json:"ids"`
	Explained []*explained `json:"explained"`
}

func TestUnicodeSweep(t *testing.T) {
	dir := os.Getenv("SUMIKA_TEST_SWEEP_DIR")
	if dir == "" {
		t.Skip("没设 SUMIKA_TEST_SWEEP_DIR（parity_ref.py sweep 导出的目录）：不跑 NFD、NFKD 全码点这一批")
	}
	cats := loadCategories(t)
	for _, model := range []string{"gte", "bge"} {
		t.Run(model, func(t *testing.T) {
			tk := realLoaded(t, model) // 没设 SUMIKA_TEST_MODELS_ROOT 在这里跳过
			raw, err := os.ReadFile(filepath.Join(dir, "ref-sweep-"+model+".json"))
			if err != nil {
				t.Fatalf("读参考数据：%v（先跑 parity_ref.py sweep）", err)
			}
			var ref sweepRef
			if err := json.Unmarshal(raw, &ref); err != nil {
				t.Fatal(err)
			}
			if n := len(ref.Texts); n < 10000 || len(ref.IDs) != n || len(ref.Explained) != n {
				t.Fatalf("参考数据条数不对：文字 %d、id %d、说明 %d", n, len(ref.IDs), len(ref.Explained))
			}
			same, compared, skipped := 0, 0, 0
			byCat := map[string]int{}
			var bad []string
			for i, text := range ref.Texts {
				if ref.IDs[i] == nil {
					skipped++
					continue
				}
				compared++
				got, err := tk.EncodeSingle(text, singleMaxTokens(model))
				if err != nil {
					t.Fatalf("%s #%d %q：%v", model, i, text, err)
				}
				if slices.Equal(got, ref.IDs[i]) {
					same++
					continue
				}
				names, rest := PresentCategories(cats, model, text)
				expl := ref.Explained[i]
				switch {
				case len(names) == 0:
					bad = append(bad, "归不到类别 "+describeSweep(i, text, got, ref.IDs[i]))
				case expl == nil || !slices.Equal(expl.Cats, names) || expl.Error != "":
					bad = append(bad, "类别和参考数据对不上 "+describeSweep(i, text, got, ref.IDs[i]))
				default:
					gotRest, err := tk.EncodeSingle(rest[0], singleMaxTokens(model))
					if err != nil || !slices.Equal(gotRest, expl.IDs) {
						bad = append(bad, "去掉类别字符以后仍不一致 "+describeSweep(i, text, got, ref.IDs[i]))
						continue
					}
					byCat[strings.Join(names, "+")]++
				}
			}
			var cs []string
			for c, n := range byCat {
				cs = append(cs, fmt.Sprintf("%s %d", c, n))
			}
			sort.Strings(cs)
			t.Logf("SWEEP %s（Python Unicode %s）：%d 条，逐个相同 %d，已解释 %d（%s），未解释 %d，Python 抛错跳过 %d",
				model, ref.Unicode, compared, same, compared-same-len(bad), strings.Join(cs, "，"), len(bad), skipped)
			for i, b := range bad {
				if i == 12 {
					t.Errorf("……还有 %d 条", len(bad)-12)
					break
				}
				t.Error(b)
			}
		})
	}
}

func describeSweep(i int, text string, got, want []int) string {
	var sb strings.Builder
	for _, r := range text {
		fmt.Fprintf(&sb, "U+%04X ", r)
	}
	return fmt.Sprintf("#%d %s：Go %v，Python %v", i, strings.TrimSpace(sb.String()), head(got), head(want))
}
