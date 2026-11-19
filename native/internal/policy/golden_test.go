package policy

// 审批判定的对拍（规格-Go后台 四点七 2d）：testdata/*.json 是 scripts/go-golden-policy.test.ts 让 Bun 版算出来的（输入带结果），这里逐条比

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
	"unicode"

	"sumika-native/internal/jsjson"
)

func readGolden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestRiskSameAsBun(t *testing.T) {
	windowsPathsOnly(t)
	var g struct {
		SelfPorts   []int `json:"selfPorts"`
		RawCommands []struct {
			Cmd            string `json:"cmd"`
			ReadOnly       bool   `json:"readOnly"`
			Catastrophic   bool   `json:"catastrophic"`
			DeletesUserDir bool   `json:"deletesUserDir"`
			DeleteVerb     bool   `json:"deleteVerb"`
			SelfEndpoint   bool   `json:"selfEndpoint"`
		} `json:"commands"`
		Inside []struct {
			Target  string   `json:"target"`
			Dirs    []string `json:"dirs"`
			Workdir string   `json:"workdir"`
			Inside  bool     `json:"inside"`
		} `json:"inside"`
	}
	readGolden(t, "risk", &g)
	bad := 0
	for _, c := range g.RawCommands {
		got := [5]bool{IsReadOnlyCommand(c.Cmd), IsCatastrophic(c.Cmd), DeletesUserDir(c.Cmd), HasDeleteVerb(c.Cmd), TouchesSelfEndpoint(c.Cmd, g.SelfPorts)}
		want := [5]bool{c.ReadOnly, c.Catastrophic, c.DeletesUserDir, c.DeleteVerb, c.SelfEndpoint}
		if got != want {
			bad++
			t.Errorf("%q：Go 只读/灾难/删用户目录/删除动词/碰端口 = %v，Bun = %v", c.Cmd, got, want)
		}
	}
	for _, c := range g.Inside {
		if got := IsInside(c.Target, c.Dirs, c.Workdir); got != c.Inside {
			bad++
			t.Errorf("IsInside(%q, %q, %q)：Go %v，Bun %v", c.Target, c.Dirs, c.Workdir, got, c.Inside)
		}
	}
	if len(g.RawCommands) == 0 || len(g.Inside) == 0 {
		t.Fatal("golden 是空的")
	}
	t.Logf("命令 %d 条、路径 %d 条，不一样的 %d 条", len(g.RawCommands), len(g.Inside), bad)
}

type shellGolden struct {
	Cmd      string          `json:"cmd"`
	Analysis *Analysis       `json:"analysis"`
	Verdict  json.RawMessage `json:"verdict"`
}

func sameAnalysis(got, want *Analysis) bool {
	return reflect.DeepEqual(got, want)
}

// sameExceptErrorLanguage：pwsh 的解析报错跟着系统界面语言走。基线是中文报错、这次一条中文都没有时，
// 只要求条数一样，其余字段照旧逐项比；两边都是中文（或基线不是中文）时不放宽。
func sameExceptErrorLanguage(got, want *Analysis) bool {
	if got == nil || want == nil || len(got.Errors) == 0 || len(got.Errors) != len(want.Errors) {
		return false
	}
	for i := range want.Errors {
		if !hasHan(want.Errors[i]) || hasHan(got.Errors[i]) {
			return false
		}
	}
	g, w := *got, *want
	g.Errors, w.Errors = nil, nil
	return reflect.DeepEqual(&g, &w)
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// 对拍不能把同样条数的另一种解析错误当成相同。
func TestAnalyzerComparisonRejectsChangedError(t *testing.T) {
	var cases []shellGolden
	readGolden(t, "shell", &cases)
	for _, c := range cases {
		if c.Analysis == nil || len(c.Analysis.Errors) == 0 {
			continue
		}
		changed := *c.Analysis
		changed.Errors = append([]string(nil), changed.Errors...)
		changed.Errors[0] += "（另一种错误）"
		if sameAnalysis(&changed, c.Analysis) {
			t.Fatalf("错误条数一样但错误不同，对拍必须报不一样：%q / %q", changed.Errors, c.Analysis.Errors)
		}
		return
	}
	t.Fatal("golden 里没有解析错误样本，没测到错误比较")
}

// Go 起的 pwsh 拆出来的和 Bun 的一样（同一段解析脚本、同一个 pwsh）
func TestAnalyzerSameAsBun(t *testing.T) {
	windowsPathsOnly(t)
	var g []shellGolden
	readGolden(t, "shell", &g)
	a := NewAnalyzer()
	defer a.Close()
	bad := 0
	for _, c := range g {
		got := a.Analyze(c.Cmd, time.Minute) // 比的是拆出来的内容；时限只防卡死，机器忙时冷启动也要十几秒
		equal := sameAnalysis(got, c.Analysis)
		if !equal && sameExceptErrorLanguage(got, c.Analysis) {
			t.Logf("%q 只有解析报错的语言不同（基线是中文 pwsh 录的，这台机器的 pwsh 报别的语言），条数一样，算相同", c.Cmd)
			equal = true
		}
		if !equal {
			bad++
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(c.Analysis)
			t.Errorf("%q 拆出来不一样\n Go：%s\nBun：%s", c.Cmd, gj, wj)
		}
	}
	t.Logf("命令 %d 条，不一样的 %d 条", len(g), bad)
}

// judge 对着 Bun 拆出来的结果算，结论逐字段比
func TestJudgeSameAsBun(t *testing.T) {
	var g []shellGolden
	readGolden(t, "shell", &g)
	bad := 0
	for _, c := range g {
		if c.Analysis == nil {
			continue
		}
		got, _ := json.Marshal(Judge(*c.Analysis))
		var gv, wv any
		json.Unmarshal(got, &gv)
		json.Unmarshal(c.Verdict, &wv)
		if !reflect.DeepEqual(gv, wv) {
			bad++
			t.Errorf("%q 结论不一样\n Go：%s\nBun：%s", c.Cmd, got, c.Verdict)
		}
	}
	t.Logf("命令 %d 条，不一样的 %d 条", len(g), bad)
}

// 判定、"同一类"、能不能"始终"、碰到的外面、要解析联接的路径、工具路径，逐组比
func TestDecideSameAsBun(t *testing.T) {
	windowsPathsOnly(t)
	var g struct {
		Base Env `json:"base"`
		Out  []struct {
			Tool        string          `json:"tool"`
			Args        json.RawMessage `json:"args"`
			Mode        string          `json:"mode"`
			Audience    Audience        `json:"audience"`
			Granted     string          `json:"granted"`
			GrantDirs   []string        `json:"grantDirs"`
			Shell       *Verdict        `json:"shell"`
			ShellFailed bool            `json:"shellFailed"`
			Decision    Decision        `json:"decision"`
			Kind        *Kind           `json:"kind"`
			CanAlways   bool            `json:"canAlways"`
			Outside     struct {
				Paths []string `json:"paths"`
				Blind bool     `json:"blind"`
			} `json:"outside"`
			LinkPaths []string `json:"linkPaths"`
			ToolPaths []string `json:"toolPaths"`
		} `json:"out"`
	}
	readGolden(t, "decide", &g)
	t.Setenv("USERPROFILE", `C:\Users\someone`) // 和 Bun 那边编的用户目录一样
	bad, intended := 0, 0
	for _, c := range g.Out {
		args, err := jsjson.Parse(string(c.Args))
		if err != nil {
			t.Fatal(err)
		}
		spec, declared := Specs[c.Tool]
		if !declared {
			spec = Undeclared
		}
		// 有意不同（复核 2026-11-10）：path 不是字符串时，工具一律当 "."，判定也对齐（PathArg）；Bun 录像里还是 String(x)，
		// 所以“同一类”的目录键、联接解析路径、工具路径这三项会不同（TestNonStringPathArgAlignedWithTools 单独钉住）。
		// 判定结论、能不能“始终”、碰到的外面这几项仍然逐项比（policy 对拍没有更新开关，只放宽这几项）。
		nonString := false
		if pv, ok := args.Obj["path"]; ok && spec.Paths != nil && pv.Kind != jsjson.String && pv.Kind != jsjson.Null {
			nonString = true
			intended++
		}
		env := g.Base
		env.Mode, env.Audience, env.Declared, env.Shell, env.ShellFailed, env.Granted, env.GrantDirs = c.Mode, c.Audience, declared, c.Shell, c.ShellFailed, c.Granted, c.GrantDirs
		toolPaths := []string{}
		if spec.Paths != nil {
			for _, p := range spec.Paths(args) {
				toolPaths = append(toolPaths, ToolPath(p, env.Workdir))
			}
		}
		paths, blind := OutsideTargets(env)
		got := []any{Decide(c.Tool, spec, args, env), KindOf(c.Tool, args, env.Workdir, c.Shell), CanAlways(c.Tool, args, declared, c.Shell), paths, blind, LinkPaths(spec, args, env), toolPaths}
		want := []any{c.Decision, c.Kind, c.CanAlways, c.Outside.Paths, c.Outside.Blind, c.LinkPaths, c.ToolPaths}
		if nonString { // 只比 判定、能不能始终、外面的目录、看不清
			got, want = []any{got[0], got[2], got[3], got[4]}, []any{want[0], want[2], want[3], want[4]}
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			bad++
			if bad <= 20 {
				t.Errorf("%s %s（%s，%s%s）不一样\n Go：%s\nBun：%s", c.Tool, c.Args, c.Mode, c.Granted, map[bool]string{true: "，拆不开"}[c.ShellFailed], gj, wj)
			}
		}
	}
	t.Logf("%d 组，不一样的 %d 组，只比判定等四项的有意不同 %d 组", len(g.Out), bad, intended)
}

// 放宽只认“基线中文、这次没中文、条数一样、其余字段相同”：哪一条不满足都还是不一样
func TestSameExceptErrorLanguage(t *testing.T) {
	zh := &Analysis{Errors: []string{"表达式中缺少右括号“)”。"}}
	en := &Analysis{Errors: []string{"Missing closing ')' in expression."}}
	if !sameExceptErrorLanguage(en, zh) {
		t.Error("只有报错语言不同，该算相同")
	}
	if sameExceptErrorLanguage(&Analysis{Errors: []string{"表达式中缺少别的"}}, zh) {
		t.Error("两边都是中文但报错不同，不能放过")
	}
	if sameExceptErrorLanguage(&Analysis{Errors: []string{"a", "b"}}, zh) {
		t.Error("条数不同，不能放过")
	}
	other := *en
	other.Commands = []Command{{}}
	if sameExceptErrorLanguage(&other, zh) {
		t.Error("报错以外的字段不同，不能放过")
	}
}
