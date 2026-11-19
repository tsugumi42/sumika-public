package main

import (
	"reflect"
	"testing"
)

func TestLiteral(t *testing.T) {
	cases := []struct{ text, memory string }{
		{"前缀主人周四去蓝纸诊所复查后缀", "主人周四去蓝纸诊所复查"},
		{"主人 周四，去蓝纸！诊所复查", "主人周四去蓝纸诊所复查"},
		{"主人怕响铃", "主人怕响铃"},
		{"主人怕响铃声", "主人怕响铃声"},
		{"蓝纸诊所复查", "主人周四去蓝纸诊所复查"},
		{"主人喜欢teacup", "主人喜欢TeaCup"},
		{"甲乙丙丁戊己庚辛壬癸", "前面甲乙丙丁戊己庚辛壬癸后面"},
		{"甲乙丙丁戊己庚辛壬", "甲乙丙丁戊己庚辛壬癸"},
	}
	got := make([]bool, len(cases))
	for i, c := range cases {
		got[i] = literal(c.text, []Memory{{Text: c.memory}})
	}
	want := []bool{true, true, false, true, false, false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("字面边界 got=%v want=%v", got, want)
	}
}

func TestThresholdCounts(t *testing.T) {
	samples := []Sample{{Parts: []string{"正"}, ShouldBlock: true}, {Parts: []string{"负"}}, {Parts: []string{"字面"}, ShouldBlock: true}, {Parts: []string{"误拦"}}}
	evidence := map[string]Evidence{
		"正":  {Score: .5, Yes: true, Asked: true, LiteralMS: 1, EmbedMS: 2, ModelMS: 10},
		"负":  {Score: .7, Yes: false, Asked: true, LiteralMS: 1, EmbedMS: 2, ModelMS: 20},
		"字面": {Literal: true, Score: -1, LiteralMS: 1},
		"误拦": {Score: .4, Yes: true, Asked: true, LiteralMS: 1, EmbedMS: 2, ModelMS: 30},
	}
	var got []Counts
	for _, threshold := range []float64{.3, .5, .55} {
		var decisions []Decision
		for _, s := range samples {
			d, err := decide(s, "C", threshold, func(text string) (Evidence, error) { return evidence[text], nil })
			if err != nil {
				t.Fatal(err)
			}
			decisions = append(decisions, d)
		}
		got = append(got, summarize(samples, decisions))
	}
	want := []Counts{{TP: 2, Pos: 2, FP: 1, Neg: 2, Calls: 3, EmbedCalls: 3, P50: 13, P95: 33}, {TP: 2, Pos: 2, Neg: 2, Calls: 2, EmbedCalls: 3, P50: 3, P95: 23}, {TP: 1, Pos: 2, Neg: 2, Calls: 1, EmbedCalls: 3, P50: 3, P95: 23}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("扫描计数 got=%+v want=%+v", got, want)
	}
}

// 量测和线上按同一个口径比：float32 的 0.70 转成 float64 比 0.70 小一点，旧写法会当成没过门槛。
func TestThresholdExactlyAtFloat32(t *testing.T) {
	s := Sample{Parts: []string{"正好"}, ShouldBlock: true}
	e := Evidence{Score: float64(float32(.7)), Yes: true, Asked: true}
	d, err := decide(s, "C", .7, func(string) (Evidence, error) { return e, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !d.Blocked || d.Calls != 1 {
		t.Fatalf("得分正好 0.70 应当过门槛、问模型并拦下：%+v", d)
	}
}

func TestPassedPrefix(t *testing.T) {
	s := Sample{Parts: []string{"前段", "后段"}, ShouldBlock: true}
	var got []Decision
	for _, firstBlock := range []bool{false, true} {
		d, err := decide(s, "C", .5, func(text string) (Evidence, error) {
			switch text {
			case "前段":
				return Evidence{Literal: firstBlock, Score: -1}, nil
			case "前段\n后段":
				return Evidence{Literal: true, Score: -1}, nil
			default:
				return Evidence{Score: -1}, nil
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	want := []Decision{{Blocked: true, SegmentMS: []float64{0, 0}}, {PrefixFalseBlocks: 1, SegmentMS: []float64{0, 0}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("已放行累计 got=%+v want=%+v", got, want)
	}
}
