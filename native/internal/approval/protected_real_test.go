package approval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/junction"
)

// 独立复核（2026-11-11 工具）中 3：数据目录本身是联接时，用真实路径去碰它，字面比对认不出，read、grep、find 判放行，edit、write 还给“这个对话 / 始终”。
// 受保护目录的清单要把每个目录的真实路径也并进去。
func TestProtectedDirIsJunctionRealPathStillProtected(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	for _, key := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(key, home)
	}
	base := t.TempDir()
	if r, err := RealStrict(base); err == nil {
		base = r
	}
	real, link, wd := filepath.Join(base, "realdata"), filepath.Join(base, "data"), filepath.Join(base, "work")
	for _, d := range []string{filepath.Join(real, "config"), wd, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(real, "config", "sumika.yml"), []byte("S"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := junction.Create(link, real); err != nil {
		t.Fatalf("建不了联接：%v", err)
	}
	an := policy.NewAnalyzer()
	defer an.Close()
	g := NewGateWith(link, an)

	lower := map[string]bool{}
	for _, d := range g.ProtectedDirs {
		lower[strings.ToLower(d)] = true
	}
	if !lower[strings.ToLower(link)] || !lower[strings.ToLower(real)] {
		t.Errorf("受保护目录清单里要有联接本身和它的真实路径：%v", g.ProtectedDirs)
	}
	if len(lower) != len(g.ProtectedDirs) {
		t.Errorf("清单里有重复：%v", g.ProtectedDirs)
	}

	identity := kernel.Invocation{Scope: kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "fixture-desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "fixture-authority", AuthorityRevision: 1}, InputID: "in", RootID: "root", RunID: "run", StepID: "step", ToolID: "tool", Generation: 1, Kind: kernel.KindForeground}
	file := filepath.Join(real, "config", "sumika.yml")
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, c := range []struct{ tool, args string }{
			{"read", `{"path":` + jsonQuote(file) + `}`},
			{"grep", `{"pattern":"S","path":` + jsonQuote(filepath.Join(real, "config")) + `}`},
			{"find", `{"pattern":"*","path":` + jsonQuote(real) + `}`},
			{"edit", `{"path":` + jsonQuote(file) + `,"edits":[{"oldText":"S","newText":"T"}]}`},
			{"write", `{"path":` + jsonQuote(file) + `,"content":"x"}`},
		} {
			g.Mode = mode
			var asked []string
			g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
				asked = append(asked, strings.Join(req.Options, ","))
				return Decision{Kind: "cancelled"}
			}
			args, err := jsjson.Parse(c.args)
			if err != nil {
				t.Fatal(err)
			}
			blocked, _ := g.Check(context.Background(), wd, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, "console:suzu", Call{Identity: identity, ID: "call-1", Name: c.tool, Args: args})
			if !blocked || len(asked) != 1 || asked[0] != "" {
				t.Errorf("%s %s：真实路径要问且不给“这个对话 / 始终”，得到 blocked=%v 问了 %v", mode, c.tool, blocked, asked)
			}
		}
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
