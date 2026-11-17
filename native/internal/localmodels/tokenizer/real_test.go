package tokenizer

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 用真 tokenizer.json 的测试（SUMIKA_TEST_MODELS_ROOT 没设就跳过）。

const realVocabSize = 250002 // gte 和 bge 的词表条数（两份词表逐项相同，2026-11-02 核过）

// realSpec 是登记表里这两个模型的 tokenizer 约束
func realSpec(name string) Spec {
	return Spec{SHA256: realTokenizers[name].sha256}
}

var realCache sync.Map // name → *Tokenizer

// realLoaded 加载真文件的分词器，同一个进程里每个只加载一次
func realLoaded(t testing.TB, name string) *Tokenizer {
	t.Helper()
	raw := realTokenizerJSON(t, name) // 没设环境变量在这里跳过
	if tk, ok := realCache.Load(name); ok {
		return tk.(*Tokenizer)
	}
	tk, err := Load(realSpec(name), raw)
	if err != nil {
		t.Fatalf("加载 %s：%v", name, err)
	}
	realCache.Store(name, tk)
	return tk
}

func TestSpecialLiteralsAreTextReal(t *testing.T) {
	for _, name := range []string{"gte", "bge"} {
		t.Run(name, func(t *testing.T) {
			checkSpecialLiterals(t, realLoaded(t, name), realTokenizerJSON(t, name))
		})
	}
}

// 上万条随机串（含非法 UTF-8、NUL、代理对、超长）不 panic、不报错，id 都在词表范围里
func checkNoPanic(t *testing.T, tk *Tokenizer, vocab int, seed uint64, n int) {
	t.Helper()
	for i, s := range randomStrings(seed, n) {
		ids, err := tk.EncodeSingle(s, 256)
		if err != nil {
			t.Fatalf("第 %d 条随机串（%d 字节）：%v", i, len(s), err)
		}
		for _, id := range ids {
			if id < 0 || id >= vocab {
				t.Fatalf("第 %d 条随机串：id %d 不在词表范围 [0, %d)", i, id, vocab)
			}
		}
	}
}

func TestTokenizerNoPanic(t *testing.T) {
	for _, flavor := range []string{"gte", "bge"} {
		t.Run("mini-"+flavor, func(t *testing.T) {
			tk, _ := miniLoaded(t, flavor)
			checkNoPanic(t, tk, len(miniTokenizer(flavor).Model.Vocab), 7, 20000)
		})
	}
	for _, name := range []string{"gte", "bge"} {
		t.Run(name, func(t *testing.T) {
			checkNoPanic(t, realLoaded(t, name), realVocabSize, 7, 20000)
		})
	}
}

// 分词耗时不随输入长度平方增长（规格 GM1 验收 7 的最后一句）：输入长 4 倍，耗时该约 4 倍，平方增长是 16 倍。
// 上限放在 8 倍（4 倍的两倍余量）；最短那档耗时太小量不准的就不比。
func TestEncodeScaling(t *testing.T) {
	tk := realLoaded(t, "gte")
	inputs := []struct{ name, unit string }{
		{"汉字无空格", "今天晚上想吃火锅，你有什么推荐的店吗？"},
		{"英文带空格", "the quick brown fox jumps over the lazy dog "},
		{"零宽字符", "\u200b"},
	}
	for _, in := range inputs {
		times := map[int]time.Duration{}
		var line []string
		for _, n := range []int{8192, 32768, 131072} {
			text := string([]rune(strings.Repeat(in.unit, n/len([]rune(in.unit))+1))[:n])
			best := time.Hour
			for range 5 {
				t0 := time.Now()
				if _, err := tk.Content(text); err != nil {
					t.Fatal(err)
				}
				best = min(best, time.Since(t0))
			}
			times[n] = best
			line = append(line, fmt.Sprintf("%d 字 %.2f ms", n, float64(best.Microseconds())/1000))
		}
		t.Logf("SCALE %s：%s", in.name, strings.Join(line, "，"))
		if times[32768] >= 2*time.Millisecond { // 太短的量不准
			ratio := float64(times[131072]) / float64(times[32768])
			t.Logf("SCALE %s：131072 字 / 32768 字 = ×%s", in.name, strconv.FormatFloat(ratio, 'f', 2, 64))
			if ratio > 8 {
				t.Errorf("%s：输入长 4 倍，耗时变成 %.1f 倍（线性该约 4 倍，平方 16 倍）", in.name, ratio)
			}
		}
	}
}
