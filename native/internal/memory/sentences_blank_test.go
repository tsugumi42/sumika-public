package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"sumika-native/internal/embed"
)

// 切句的口径要和嵌入服务判空的口径是同一个集合（规格-记忆写入上线 W1a，复核发现）：
// 服务（Python 的 server.py 和它的 Go 版 sumika-models）用 `not text.strip()` 判空，一批里有一句"空"整批回 400。
// Go 的 strings.TrimSpace 不认 U+001C～U+001F，原来切出 ["甲。", "\x1f"] 送去，整批算不出来，预热无限重试、守卫永远 literal-pending。
// 这里的"服务眼里的空白"直接读 Python 导出的数据（engine/testdata/ref-whitespace.json），不借 pyspace 的实现，两头各验各的。

var (
	pySpaceOnce sync.Once
	pySpaceSet  map[rune]bool
)

func pythonSpaces(t testing.TB) map[rune]bool {
	t.Helper()
	pySpaceOnce.Do(func() {
		raw, err := os.ReadFile(filepath.Join("..", "localmodels", "engine", "testdata", "ref-whitespace.json"))
		if err != nil {
			t.Fatalf("读 Python 的空白集合：%v", err)
		}
		var ref struct {
			IsSpace []int `json:"isspace"`
		}
		if err := json.Unmarshal(raw, &ref); err != nil || len(ref.IsSpace) < 20 {
			t.Fatalf("Python 的空白集合读不出来：%v（%d 个）", err, len(ref.IsSpace))
		}
		pySpaceSet = map[rune]bool{}
		for _, c := range ref.IsSpace {
			pySpaceSet[rune(c)] = true
		}
	})
	if pySpaceSet == nil {
		t.Fatal("Python 的空白集合没读到")
	}
	return pySpaceSet
}

// serviceBlank 是嵌入服务眼里的"空"：全是 Python 的空白。
func serviceBlank(t testing.TB, s string) bool {
	set := pythonSpaces(t)
	for _, r := range s {
		if !set[r] {
			return false
		}
	}
	return true
}

func TestOutboundSentencesAgainstServiceBlank(t *testing.T) {
	t.Parallel()
	t.Run("具体的几句", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			in   string
			want []string
		}{
			{"甲。\x1f", []string{"甲。"}},
			{"\x1f", nil},
			{"\x1c\x1d\x1e\x1f", nil},
			{"\x1f甲。\x1e乙。", []string{"甲。", "乙。"}},
			{"乙\x1c\x1d。", []string{"乙\x1c\x1d。"}}, // 只去首尾，句子中间的控制字符留着
			{"甲。\u0085\u00a0\u3000", []string{"甲。"}},
			{"。\x1f。", nil},                                    // 只剩标点的句子本来就丢
			{"零宽\u200b结尾\u200b", []string{"零宽\u200b结尾\u200b"}}, // 零宽字符不在 Python 的集合里：照旧留着，服务也照常前向
			{"\u200b", []string{"\u200b"}},
		} {
			if got := OutboundSentences(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("OutboundSentences(%q) = %q，想要 %q", c.in, got, c.want)
			}
		}
	})

	// 对着全部码点：切出来的每一句，服务都不该判成空，首尾也不该留着服务会剥掉的字符
	t.Run("全部码点", func(t *testing.T) {
		t.Parallel()
		set := pythonSpaces(t)
		check := func(in string, out []string) {
			for _, s := range out {
				if serviceBlank(t, s) {
					t.Fatalf("OutboundSentences(%q) 切出了服务眼里的空句 %q", in, s)
				}
				first, _ := utf8.DecodeRuneInString(s)
				last, _ := utf8.DecodeLastRuneInString(s)
				if set[first] || set[last] {
					t.Fatalf("OutboundSentences(%q) 的句子 %q 首尾还有服务会剥掉的字符", in, s)
				}
			}
		}
		for r := rune(0); r <= 0x10ffff; r++ {
			if r >= 0xd800 && r <= 0xdfff {
				continue
			}
			c := string(r)
			check("甲。"+c, OutboundSentences("甲。"+c))
			if set[r] {
				if got := OutboundSentences("甲。" + c); !reflect.DeepEqual(got, []string{"甲。"}) {
					t.Fatalf("U+%04X 是 Python 的空白，结尾的它该被去掉：%q", r, got)
				}
				if got := OutboundSentences(c); len(got) != 0 {
					t.Fatalf("只有 U+%04X 一个字符不该切出句子：%q", r, got)
				}
				continue
			}
			if !strings.ContainsRune("。！？!?；;", r) {
				mid := "甲" + c + "乙"
				if got := OutboundSentences(mid); !reflect.DeepEqual(got, []string{mid}) {
					t.Fatalf("U+%04X 不是 Python 的空白，夹在句子中间该原样留着：%q", r, got)
				}
			}
		}
	})
}

// blankRejectingEmbed 扮演嵌入服务：任何一批里有一句 Python 眼里的空白，整批回 400（和 server.py、sumika-models 一样）
type blankRejectingEmbed struct {
	*warmEmbed
	t        testing.TB
	mu2      sync.Mutex
	rejected int
}

var errServiceBlank = errors.New("编的 400：Empty input strings are not supported")

func (e *blankRejectingEmbed) reject(texts []string) error {
	for _, s := range texts {
		if serviceBlank(e.t, s) {
			e.mu2.Lock()
			e.rejected++
			e.mu2.Unlock()
			return errServiceBlank
		}
	}
	return nil
}

func (e *blankRejectingEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := e.reject(texts); err != nil {
		return nil, err
	}
	return e.warmEmbed.Embed(ctx, texts)
}

func (e *blankRejectingEmbed) EmbedTyped(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	if err := e.reject(texts); err != nil {
		return nil, err
	}
	return e.warmEmbed.EmbedTyped(ctx, kind, texts)
}

var _ embed.Typed = (*blankRejectingEmbed)(nil)

// 记忆里有一句只含 U+001F 的话（"甲。\x1f"）：预热不能栽在这一句上。
// 旧代码切出 ["甲。", "\x1f"]，服务整批 400，预热每次失败、翻倍间隔无限重试，GuardLevel 永远 literal-pending
func TestWarmupWithServiceBlankSentence(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "甲。\x1f", "owner", "owner"},
		{"2", "fact", "active", "\x1f", "owner", "owner"},
		{"3", "fact", "active", "他怕黑。\x1c\x1d\x1e\x1f她喜欢猫。", "owner", "owner"},
		{"4", "fact", "active", "正常的一句话。", "owner", "owner"},
	}, nil)
	e := &blankRejectingEmbed{warmEmbed: newWarmEmbed("预热测试嵌入"), t: t}
	sink := &logSink{}
	m, err := Open(context.Background(), file, e, nil, Options{
		Character: "suzune", Logf: sink.logf, Gen: &outboundFakeGen{reply: `{"leaked":"no"}`}, Model: func() string { return "编的后台模型" },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if got := m.GuardLevel(); got != "literal-pending" {
		t.Fatalf("还没预热：%q", got)
	}
	m.EmbeddingReady()
	waitGuard(t, m, "full")
	e.mu2.Lock()
	rejected := e.rejected
	e.mu2.Unlock()
	if rejected != 0 {
		t.Fatalf("送进去的话里不该有服务眼里的空白：被拒 %d 次\n%s", rejected, sink.String())
	}
	// "甲。" 一句；"\x1f" 没有句子；"他怕黑。" "她喜欢猫。" 两句；"正常的一句话。" 一句
	if n := sentenceRows(t, m, ""); n != 4 {
		t.Fatalf("该有 4 行句向量：%d", n)
	}
	// 守卫判断这几条记忆也不报错（走 normal，不走 embed-down）
	got, err := m.CheckOutbound(context.Background(), "今天天气不错。", Audience{Channel: "qq", Conversation: "qq:100:group:200", Scene: "group"})
	if err != nil || m.GuardRoute() != "normal" {
		t.Fatalf("守卫判断：%v %v %s", got, err, m.GuardRoute())
	}
}

// 整条正文都是服务眼里的空白（"\x1f"）的条目，补 memory_vectors 时也不能让一批都算不出来
func TestSyncVectorsSkipsServiceBlankText(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "\x1f", "owner", "owner"},
		{"2", "fact", "active", "正常的一条记忆。", "owner", "owner"},
	}, nil)
	e := &blankRejectingEmbed{warmEmbed: newWarmEmbed("预热测试嵌入"), t: t}
	m, err := Open(context.Background(), file, e, nil, Options{Character: "suzune", Logf: (&logSink{}).logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	items, _ := m.snapshot()
	computed, err := m.index.SyncVectors(context.Background(), items, e, VectorBatch)
	if err != nil {
		t.Fatalf("补向量不该栽在空白正文上：%v", err)
	}
	e.mu2.Lock()
	rejected := e.rejected
	e.mu2.Unlock()
	if rejected != 0 {
		t.Fatalf("不该把服务眼里的空白送去嵌入：被拒 %d 次", rejected)
	}
	var n int
	if err := m.index.db.QueryRow(`SELECT count(*) FROM memory_vectors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if miss, err := m.index.MissingVectors(items); err != nil || miss != 0 {
		t.Fatalf("空白正文不该算缺向量：%d %v", miss, err)
	}
	if _, err := m.syncVectors(context.Background()); err != nil || m.EmbeddingDegraded() {
		t.Fatalf("库里有空白正文的条目，降级标志不该一直亮着：%v %v", err, m.EmbeddingDegraded())
	}
	if n != 1 {
		t.Fatalf("库里该只有正常那一条的向量：%d 行（这一趟算了 %d）", n, computed)
	}
}
