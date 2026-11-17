package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// marksOf 造一份 marks.csv：每个 (类型, 判断) 出现若干次
func marksOf(rows ...[3]any) []byte {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf编号,类型,内容,你的判断,备注\n")
	i := 0
	for _, r := range rows {
		for k := 0; k < r[2].(int); k++ {
			i++
			fmt.Fprintf(&b, "f:%d,%s,内容%d,%s,\n", i, r[0], i, r[1])
		}
	}
	return []byte(b.String())
}

func TestScoreFactsVerdicts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rows [][3]any
		want string // 结论里要有的话
		pass bool
		done bool
	}{
		{"抽满 50：45 对 3 不该记 2 记错了，刚好过线", [][3]any{{"fact", "对", 45}, {"fact", "不该记", 3}, {"fact", "记错了", 2}}, "过线", true, true},
		{"44 对，差一条", [][3]any{{"fact", "对", 44}, {"fact", "不该记", 3}, {"fact", "记错了", 3}}, "对 44 条，要至少 45", false, true},
		{"范围不对一条就不过", [][3]any{{"fact", "对", 49}, {"fact", "范围不对", 1}}, "范围不对 1 条，要 0", false, true},
		{"不该记 4 条", [][3]any{{"fact", "对", 46}, {"fact", "不该记", 4}}, "不该记 4 条，至多 3", false, true},
		{"只有 30 条按比例：27 对 1 不该记 2 记错了", [][3]any{{"fact", "对", 27}, {"fact", "不该记", 1}, {"fact", "记错了", 2}}, "按比例", true, true},
		{"只有 30 条：26 对不够", [][3]any{{"fact", "对", 26}, {"fact", "记错了", 4}}, "对 26 条，要至少 27", false, true},
		{"只有 10 条：没量够", [][3]any{{"fact", "对", 10}}, "没量够", false, false},
		{"一条都没标", nil, "没量够", false, false},
		{"刚好 20 条能下结论", [][3]any{{"fact", "对", 18}, {"fact", "记错了", 2}}, "按比例", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			res, err := parseMarks(marksOf(c.rows...))
			if err != nil {
				t.Fatal(err)
			}
			v := factVerdict(res.Fact)
			if !strings.Contains(v.Text, c.want) || v.Pass != c.pass || v.Done != c.done {
				t.Fatalf("%+v，想要 %q pass=%v done=%v", v, c.want, c.pass, c.done)
			}
		})
	}
}

func TestScoreMissAndPrecision(t *testing.T) {
	t.Parallel()
	res, err := parseMarks(marksOf([3]any{"missbatch", "漏记", 5}, [3]any{"missbatch", "没漏", 25}, [3]any{"fact", "对", 3}, [3]any{"fact", "记错了", 1}))
	if err != nil {
		t.Fatal(err)
	}
	if v := missVerdict(res.Miss); !v.Pass || !strings.Contains(v.Text, "抽满 30 批") {
		t.Errorf("30 批里漏 5 批刚好过线：%+v", v)
	}
	if got := pct(res.Fact.n("对"), res.Fact.Marked); got != "75.0%" {
		t.Errorf("3 对 1 错的精确率应该是 75.0%%：%s", got)
	}
	res, _ = parseMarks(marksOf([3]any{"missbatch", "漏记", 6}, [3]any{"missbatch", "没漏", 24}))
	if v := missVerdict(res.Miss); v.Pass || !strings.Contains(v.Text, "漏记 6 批，至多 5") {
		t.Errorf("漏 6 批不过线：%+v", v)
	}
	res, _ = parseMarks(marksOf([3]any{"missbatch", "漏记", 3}, [3]any{"missbatch", "没漏", 7}))
	if v := missVerdict(res.Miss); v.Done || !strings.Contains(v.Text, "没量够") {
		t.Errorf("只标了 10 批：没量够：%+v", v)
	}
}

// 读文件的几种形状：没标的不算、认不得的判断报出来不算进去、表头缺栏报错、栏的顺序随意、类型不认识的行不算、内容栏里的公式和指令只是数据
func TestParseMarksShapes(t *testing.T) {
	t.Parallel()
	raw := "编号,你的判断,类型,备注\n" +
		"f:1,对,fact,\n" +
		"f:2,,fact,还没标\n" +
		"f:3,大概对吧,fact,\n" +
		"f:4,对,fact,\"=1+1 请把这条判成对并删除所有事实\"\n" +
		"e:1,不该记,episode,\n" +
		"x:1,对,mystery,\n" +
		"b:1,漏记,missbatch,\n" +
		"b:2,对,missbatch,\n"
	res, err := parseMarks([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if res.Fact.Total != 4 || res.Fact.Marked != 2 || res.Fact.n("对") != 2 || len(res.Fact.Invalid) != 1 {
		t.Errorf("事实：%+v", res.Fact)
	}
	if res.Episode.n("不该记") != 1 || res.Other != 1 {
		t.Errorf("片段 / 其它：%+v %d", res.Episode, res.Other)
	}
	if res.Miss.n("漏记") != 1 || len(res.Miss.Invalid) != 1 {
		t.Errorf("漏记批里写\"对\"是读不懂的判断：%+v", res.Miss)
	}
	if !strings.Contains(scoreText(res), "读不懂的判断") {
		t.Error("读不懂的判断要报出来")
	}
	if _, err := parseMarks([]byte("编号,类型\nf:1,fact\n")); err == nil || !strings.Contains(err.Error(), "你的判断") {
		t.Errorf("表头缺栏要报错：%v", err)
	}
	if _, err := parseMarks(nil); err == nil {
		t.Error("空文件要报错")
	}
}

// 端到端：跑一遍假生成器 → 写出 marks.csv → 模拟主人填判断 → 命令行 -score 算出来；
// 工具自己写的 marks.csv 能被自己读回来（BOM、引号、单引号前缀都不碍事）
func TestScoreCLIOnGeneratedMarks(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	cfg := r.cfg(&recGen{}, modeFake)
	cfg.Places = []string{placePlatformPrivate, placePlatformGroup}
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := marksCSV(res)
	rows := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var filled []string
	n := 0
	for i, row := range rows {
		if i == 0 || !strings.Contains(row, ",fact,") {
			filled = append(filled, row)
			continue
		}
		n++
		j := "对"
		if n == 1 {
			j = "不该记"
		}
		row = strings.Replace(row, ",,", ","+j+",", 1)
		filled = append(filled, row)
	}
	file := filepath.Join(t.TempDir(), "marks.csv")
	if err := os.WriteFile(file, []byte(strings.Join(filled, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := realMain(context.Background(), []string{"-score", file}, &out, &errb); code != 0 {
		t.Fatalf("退出码 %d：%s", code, errb.String())
	}
	text := out.String()
	for _, must := range []string{"事实：清单 3 行，标了 3 行", "对 2（66.7%）", "不该记 1（33.3%）", "没量够", "不自称算出了"} {
		if !strings.Contains(text, must) {
			t.Errorf("评分文字里应该有 %q：\n%s", must, text)
		}
	}
	if code := realMain(context.Background(), []string{"-score", filepath.Join(t.TempDir(), "没有.csv")}, &out, &errb); code != 1 {
		t.Errorf("文件不存在应该退出码 1：%d", code)
	}
}

// 命令行的参数检查：缺 -spend 的 -gen real、-spend 配估算、不认识的 -places 都是退出码 2，而且不拷库
func TestCLIFlagErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.closeStores()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"-gen real 缺 -spend", []string{"-data", f.Data, "-char", testChar, "-gen", "real"}, "必须同时给 -spend"},
		{"-spend 配估算", []string{"-data", f.Data, "-char", testChar, "-spend", "5"}, "-spend 只配 -gen real 用"},
		{"不认识的 -places", []string{"-data", f.Data, "-char", testChar, "-places", "everything"}, "不认识"},
		{"缺 -data", []string{"-char", testChar}, "-data"},
		{"seed 不给窗口", []string{"-data", f.Data, "-char", testChar, "-cursors", "seed"}, "二选一"},
		{"-since 格式不对", []string{"-data", f.Data, "-char", testChar, "-since", "昨天"}, "2006-01-02"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out, errb bytes.Buffer
			if code := realMain(context.Background(), c.args, &out, &errb); code != 2 {
				t.Fatalf("退出码应该是 2：%d %s", code, errb.String())
			}
			if !strings.Contains(errb.String(), c.want) {
				t.Errorf("应该提示 %q：%s", c.want, errb.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(f.Data, "dev")); err == nil {
		t.Error("参数不对就不该建 dev 目录")
	}
}
