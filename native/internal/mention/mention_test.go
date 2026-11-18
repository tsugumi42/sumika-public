package mention

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var cands = []Candidate{{"a", "小A"}, {"b", "小B"}, {"c", "小C"}}

func TestResolveTable(t *testing.T) {
	cases := []struct {
		name    string
		blocks  []Block
		cands   []Candidate
		targets []string
		blocksN map[string][]int
		err     error
		errBlk  int
	}{
		{name: "单指", blocks: []Block{{Mentions: []string{"b"}}}, cands: cands, targets: []string{"b"}, blocksN: map[string][]int{"b": {0}}},
		{name: "按第一次出现排序", blocks: []Block{{Mentions: []string{"c"}}, {Mentions: []string{"a", "c"}}}, cands: cands, targets: []string{"c", "a"}, blocksN: map[string][]int{"c": {0, 1}, "a": {1}}},
		{name: "同块重复只记一次", blocks: []Block{{Mentions: []string{"a", "a"}}}, cands: cands, targets: []string{"a"}, blocksN: map[string][]int{"a": {0}}},
		{name: "所有人按候选顺序展开", blocks: []Block{{All: true}}, cands: cands, targets: []string{"a", "b", "c"}, blocksN: map[string][]int{"a": {0}, "b": {0}, "c": {0}}},
		{name: "所有人和单指重叠合成一条", blocks: []Block{{Mentions: []string{"b"}}, {All: true}}, cands: cands, targets: []string{"b", "a", "c"}, blocksN: map[string][]int{"b": {0, 1}, "a": {1}, "c": {1}}},
		{name: "所有人加单指（存档里展开的结果）", blocks: []Block{{Mentions: []string{"a", "b", "c"}, All: true}}, cands: cands, targets: []string{"a", "b", "c"}, blocksN: map[string][]int{"a": {0}, "b": {0}, "c": {0}}},
		{name: "候选重复", blocks: []Block{{All: true}}, cands: []Candidate{{"a", "x"}, {"a", "y"}}, targets: []string{"a"}, blocksN: map[string][]int{"a": {0}}},
		{name: "单个候选（QQ 私聊）", blocks: []Block{{Mentions: []string{"a"}}}, cands: cands[:1], targets: []string{"a"}, blocksN: map[string][]int{"a": {0}}},
		{name: "空块", blocks: []Block{{Mentions: []string{"a"}}, {}}, cands: cands, err: ErrEmptyTarget, errBlk: 1},
		{name: "空字符串对象", blocks: []Block{{Mentions: []string{" "}}}, cands: cands, err: ErrEmptyTarget},
		{name: "非成员", blocks: []Block{{Mentions: []string{"z"}}}, cands: cands, err: ErrNotCandidate},
		{name: "非成员在第二块", blocks: []Block{{Mentions: []string{"a"}}, {Mentions: []string{"a", "z"}}}, cands: cands, err: ErrNotCandidate, errBlk: 1},
		{name: "主人自己", blocks: []Block{{Mentions: []string{"owner"}}}, cands: cands, err: ErrOwner},
		{name: "名字不能当对象", blocks: []Block{{Mentions: []string{"小A"}}}, cands: cands, err: ErrNotCandidate},
		{name: "没有候选时的所有人", blocks: []Block{{All: true}}, cands: nil, err: ErrNoCandidates},
		{name: "没有块", blocks: nil, cands: cands, targets: nil, blocksN: map[string][]int{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Resolve(c.blocks, c.cands)
			if c.err != nil {
				var be *BlockError
				if !errors.Is(err, c.err) || !errors.As(err, &be) || be.Block != c.errBlk {
					t.Fatalf("err = %v，要 %v 且在第 %d 块", err, c.err, c.errBlk+1)
				}
				if !strings.HasPrefix(err.Error(), "第 ") {
					t.Fatalf("错误要以“第 N 块：”开头：%q", err)
				}
				if len(p.Targets) != 0 {
					t.Fatalf("出错时不能留一半的目标：%v", p.Targets)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Targets, c.targets) || !reflect.DeepEqual(p.Blocks, c.blocksN) {
				t.Fatalf("targets=%v blocks=%v，要 %v %v", p.Targets, p.Blocks, c.targets, c.blocksN)
			}
		})
	}
}

// 只有被指到的角色会出现在 Targets 里：没被 @ 的候选绝不能被唤起。
func TestResolveNeverAddsUnmentioned(t *testing.T) {
	p, err := Resolve([]Block{{Mentions: []string{"b"}}}, cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || p.Targets[0] != "b" {
		t.Fatalf("只指了 b：%v", p.Targets)
	}
}

func TestResolveToken(t *testing.T) {
	dup := []Candidate{{"a", "同名"}, {"b", "同名"}, {"c", "小C"}}
	for _, c := range []struct {
		tok  string
		cs   []Candidate
		want string
		err  bool
	}{
		{"a", cands, "a", false}, {"小B", cands, "b", false}, {"nobody", cands, "", false}, {"", cands, "", false},
		{"同名", dup, "", true}, {"a", dup, "a", false}, {"小C", dup, "c", false},
	} {
		got, err := ResolveToken(c.tok, c.cs)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%q: got %q %v", c.tok, got, err)
		}
		if err != nil && !errors.Is(err, ErrAmbiguousName) {
			t.Errorf("%q: 要 ErrAmbiguousName：%v", c.tok, err)
		}
	}
}
