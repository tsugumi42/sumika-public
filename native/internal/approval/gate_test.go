package approval

// Go 审批门的回归基线最初由 sumika-bun 的 scripts/go-golden-tools.test.ts 录入，现在按 Go 规格维护（ADR-225）。
// 同时比较放行 / 拦截、卡片身份和自动放行记录；只去掉随机 ID 和绝对时间。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/junction"
)

type gateCase struct {
	Name         string              `json:"name"`
	Tool         string              `json:"tool"`
	Args         json.RawMessage     `json:"args"`
	Mode         string              `json:"mode"`
	Files        map[string]string   `json:"files"`
	Minutes      *float64            `json:"minutes"`
	Audience     *policy.Audience    `json:"audience"`
	Always       []string            `json:"always"`
	Conversation map[string][]string `json:"conversation"`
	Junction     string              `json:"junction"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// clean 去掉不稳定的 id、createdAt、expiresAt，换算等待分钟；保留会话身份，不能掩盖跨会话审批。
func clean(r Request, reason *string) map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	json.Unmarshal(b, &m)
	delete(m, "id")
	delete(m, "createdAt")
	delete(m, "expiresAt")
	delete(m, "workdir") // Go 新增的字段，Bun 基线没有；session_workdir_test.go 单独测
	m["minutes"] = float64(r.ExpiresAt-r.CreatedAt) / 60_000
	if reason != nil {
		m["reason"] = *reason
	}
	return m
}

func TestGateSameAsBun(t *testing.T) {
	var cases []gateCase
	var golden []map[string]any
	readJSON(t, "testdata/gate-cases.json", &cases)
	readJSON(t, "testdata/gate.json", &golden)
	if len(cases) != len(golden) {
		t.Fatalf("场景 %d 个，golden %d 个", len(cases), len(golden))
	}
	an := policy.NewAnalyzer()
	defer an.Close()
	// 先把拆命令的 pwsh 起好（turn 的 TestSameAsBun 也这样）：gate 只等 4 秒，冷启动在负载下会超过它，命令被当成"拆不开"，
	// 和 Bun 的"会改动：Remove-Item → a.txt"对不上（规格第七节 A35；2026-10-27 低优先级的全量跑 4 次红 2 次）。比对时照旧 4 秒，不放宽
	if an.Analyze("Get-Date", time.Minute) == nil {
		t.Fatal("拆命令的 pwsh 一分钟都没起来")
	}
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// 保留 Bun golden 的 HOME/AppData/Local/Temp 层级，所有路径都在本用例的临时目录里。
			home := filepath.Join(t.TempDir(), "home")
			tmp := filepath.Join(home, "AppData", "Local", "Temp")
			for _, key := range []string{"HOME", "USERPROFILE"} {
				t.Setenv(key, home)
			}
			for _, key := range []string{"TEMP", "TMP"} {
				t.Setenv(key, tmp)
			}
			dir, wd := filepath.Join(tmp, "data"), filepath.Join(tmp, "work")
			for _, d := range []string{dir, wd} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			outside := ""
			if c.Junction != "" {
				outside = filepath.Join(tmp, "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := junction.Create(filepath.Join(wd, c.Junction), outside); err != nil {
					t.Fatalf("建不了联接：%v", err)
				}
			}
			for rel, text := range c.Files {
				abs := filepath.Join(wd, filepath.FromSlash(rel))
				os.MkdirAll(filepath.Dir(abs), 0o755)
				os.WriteFile(abs, []byte(text), 0o644)
			}
			if len(c.Always) > 0 {
				os.MkdirAll(filepath.Join(dir, "config"), 0o755)
				var b strings.Builder
				b.WriteString("grants:\n")
				for _, k := range c.Always {
					kj, _ := json.Marshal(k)
					b.WriteString("  - key: " + string(kj) + "\n    label: x\n    tool: x\n    at: 0\n")
				}
				os.WriteFile(filepath.Join(dir, "config", "tool-grants.yml"), []byte(b.String()), 0o644)
			}
			g := NewGateWith(dir, an)
			g.Mode, g.SelfPorts, g.Analyzer = c.Mode, []int{7720, 7721}, an
			if c.Minutes != nil {
				g.ApprovalMinutes = *c.Minutes
			}
			for key, dirs := range c.Conversation {
				var ds []string
				for _, d := range dirs {
					ds = append(ds, strings.Replace(d, "<WORKDIR>", wd, 1))
				}
				g.Conv.Merge("console:suzu", Rule{Key: key, Workdir: wd, Dirs: ds})
			}
			var approval, auto any
			g.OnAuto = func(req Request, c Clearance) { auto = clean(req, &c.Reason) }
			g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
				approval = clean(req, nil)
				return Decision{Kind: "cancelled"}
			}
			aud := policy.Audience{Channel: "console", PrivateOk: true}
			if c.Audience != nil {
				aud = *c.Audience
			}
			args, err := jsjson.Parse(string(c.Args))
			if err != nil {
				t.Fatal(err)
			}
			identity := kernel.Invocation{Scope: kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "fixture-desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "fixture-authority", AuthorityRevision: 1}, InputID: "fixture-input", RootID: "fixture-root", RunID: "fixture-run", StepID: "fixture-step", ToolID: "fixture-tool", Generation: 1, Kind: kernel.KindForeground}
			block, reason := g.Check(context.Background(), wd, "suzu", aud, "console:suzu", Call{Identity: identity, ID: "call_" + c.Name, Name: c.Tool, Args: args})
			var result any
			if block {
				result = map[string]any{"block": true, "reason": reason}
			}
			got, _ := json.Marshal(map[string]any{"name": c.Name, "result": result, "approval": approval, "auto": auto})
			s := string(got)
			masks := [][2]string{{wd, "<WORKDIR>"}, {dir, "<DATA>"}}
			if outside != "" {
				masks = append(masks, [2]string{outside, "<OUTSIDE>"})
			}
			for _, f := range append(masks, [2]string{home, "<HOME>"}) {
				j, _ := json.Marshal(f[0])
				s = strings.ReplaceAll(s, string(j[1:len(j)-1]), f[1])
			}
			var gv any
			json.Unmarshal([]byte(s), &gv)
			if intendedSubject[c.Name] {
				// 有意不同（复核 2026-11-10）：卡片上写工具实际操作的位置（~ 换成用户目录），Bun 基线写的是 工作目录\~\…。
				// 其余字段照旧逐项比；这里只放过 subject，并要求它以用户目录开头。
				if sub := approvalSubject(gv); !strings.HasPrefix(sub, "<HOME>") {
					t.Errorf("subject 该以用户目录开头：%q", sub)
				}
				dropApprovalSubject(gv)
				dropApprovalSubject(golden[i])
			}
			if !reflect.DeepEqual(gv, any(golden[i])) && sameExceptParseLanguage(gv, golden[i]) {
				t.Logf("只有 pwsh 解析报错的语言不同（基线是中文 pwsh 录的），算相同")
			} else if !reflect.DeepEqual(gv, any(golden[i])) {
				want, _ := json.Marshal(golden[i])
				t.Errorf("不一样\n Go：%s\n基线：%s", s, want)
			}
		})
	}
}

// 卡片 subject 有意和 Bun 基线不同的场景：路径里的 ~ 现在按工具的办法换成用户目录
var intendedSubject = map[string]bool{"read-protected": true, "grep-protected": true}

func approvalSubject(v any) string {
	if m, ok := v.(map[string]any); ok {
		if a, ok := m["approval"].(map[string]any); ok {
			if s, ok := a["subject"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func dropApprovalSubject(v any) {
	if m, ok := v.(map[string]any); ok {
		if a, ok := m["approval"].(map[string]any); ok {
			delete(a, "subject")
		}
	}
}

// sameExceptParseLanguage：卡片 notes 里「看不清：语法有问题：…」后半句是 pwsh 的原话，跟着系统界面语言走。
// 基线那句是中文、这次一个汉字都没有时，把这两句都截成前缀再比；别的字段、别的 notes 照旧逐字比。
func sameExceptParseLanguage(got any, want map[string]any) bool {
	const prefix = "看不清：语法有问题："
	notesOf := func(v any) []any {
		m, _ := v.(map[string]any)
		ap, _ := m["approval"].(map[string]any)
		n, _ := ap["notes"].([]any)
		return n
	}
	gn, wn := notesOf(got), notesOf(any(want))
	if len(gn) == 0 || len(gn) != len(wn) {
		return false
	}
	han := func(s string) bool {
		for _, r := range s {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	changed := false
	for i := range wn {
		g, _ := gn[i].(string)
		w, _ := wn[i].(string)
		if strings.HasPrefix(g, prefix) && strings.HasPrefix(w, prefix) && han(w[len(prefix):]) && !han(g[len(prefix):]) {
			gn[i], wn[i], changed = prefix, prefix, true
		}
	}
	if !changed {
		return false
	}
	equal := reflect.DeepEqual(got, any(want))
	return equal
}
