package approval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/junction"
)

// 四种场合 × 读备份目录里的文件：
//   - 本地私聊（主人 × 主窗口）：没并进受保护目录时直接放行，并进以后每次都问；
//   - 本地群聊、QQ 私聊、QQ 群：本来就不放行（没有 Ask 可问，直接拦），并进前后都一样。
//
// 备份目录的三种形状：存在的普通目录、还不存在的目录、联接（真实路径也要受保护）。
func TestProtectBackupDirReadAsksInLocalPrivateOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	for _, key := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(key, home)
	}
	base := t.TempDir()
	if r, err := RealStrict(base); err == nil {
		base = r
	}
	wd := filepath.Join(base, "work")
	for _, d := range []string{wd, home, filepath.Join(base, "data")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plain := filepath.Join(base, "备份普通")
	missing := filepath.Join(base, "备份还没建")
	realDir, link := filepath.Join(base, "备份真身"), filepath.Join(base, "备份联接")
	for _, d := range []string{plain, realDir} {
		if err := os.MkdirAll(filepath.Join(d, "每天-1", "data", "config"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := junction.Create(link, realDir); err != nil {
		t.Skipf("建不了联接：%v", err)
	}
	shapes := []struct {
		name    string
		protect string // 并进受保护目录的那个路径
		read    string // 去读的文件
	}{
		{"存在的普通目录", plain, filepath.Join(plain, "每天-1", "data", "config", "mcp.json")},
		{"还不存在的目录", missing, filepath.Join(missing, "每天-1", "data", "config", "mcp.json")},
		{"联接，读联接路径", link, filepath.Join(link, "每天-1", "data", "config", "mcp.json")},
		{"联接，读真实路径", link, filepath.Join(realDir, "每天-1", "data", "config", "mcp.json")},
	}
	scenes := []struct {
		name   string
		aud    policy.Audience
		scope  kernel.Scope
		conv   string
		wantAs bool // 并进以后：真的弹审批卡问
	}{
		{"本地私聊", policy.Audience{Channel: "console", PrivateOk: true}, kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu", ScopeID: "console", Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}, "console:suzu", true},
		{"本地群聊", policy.Audience{Channel: "console"}, kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu:group:g", Channel: "console", Scene: "group", ActorKind: "member"}, "console:suzu:group:g", false},
		{"QQ 私聊", policy.Audience{Channel: "qq", PrivateOk: true}, kernel.Scope{CharacterID: "suzu", Conversation: "qq:private:1", Channel: "qq", Scene: "private", ActorKind: "member"}, "qq:private:1", false},
		{"QQ 群", policy.Audience{Channel: "qq"}, kernel.Scope{CharacterID: "suzu", Conversation: "qq:group:1", Channel: "qq", Scene: "group", ActorKind: "member"}, "qq:group:1", false},
	}
	an := policy.NewAnalyzer()
	defer an.Close()
	check := func(g *Gate, sc int, read string) (blocked bool, asked int) {
		g.Mode = "auto"
		g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
			asked++
			return Decision{Kind: "cancelled"}
		}
		s := scenes[sc]
		id := kernel.Invocation{Scope: s.scope, InputID: "in", RootID: "root", RunID: "run", StepID: "step", ToolID: "tool", Generation: 1, Kind: kernel.KindForeground}
		args, err := jsjson.Parse(`{"path":` + jsonQuote(read) + `}`)
		if err != nil {
			t.Fatal(err)
		}
		blocked, _ = g.Check(context.Background(), wd, "suzu", s.aud, s.conv, Call{Identity: id, ID: "call-1", Name: "read", Args: args})
		return blocked, asked
	}
	for _, sh := range shapes {
		for i, s := range scenes {
			// 没并进去：本地私聊读备份目录直接放行（这就是要堵的口子）
			before := NewGateWith(filepath.Join(base, "data"), an)
			blockedB, askedB := check(before, i, sh.read)
			after := NewGateWith(filepath.Join(base, "data"), an)
			after.Protect(sh.protect)
			blockedA, askedA := check(after, i, sh.read)
			if s.wantAs {
				if blockedB || askedB != 0 {
					t.Fatalf("%s × %s：前提不成立，没并进去时本地私聊该直接放行（blocked=%v asked=%d）", sh.name, s.name, blockedB, askedB)
				}
				if !blockedA || askedA != 1 {
					t.Errorf("%s × %s：并进受保护目录以后该问一次，得到 blocked=%v asked=%d", sh.name, s.name, blockedA, askedA)
				}
				continue
			}
			if !blockedB || !blockedA || askedA != 0 {
				t.Errorf("%s × %s：本来就不放行、并进前后都一样（拦、不弹卡）：前 %v/%d 后 %v/%d", sh.name, s.name, blockedB, askedB, blockedA, askedA)
			}
		}
	}
}

func TestProtectDedupesAndKeepsRealPath(t *testing.T) {
	base := t.TempDir()
	if r, err := RealStrict(base); err == nil {
		base = r
	}
	an := policy.NewAnalyzer()
	defer an.Close()
	g := NewGateWith(filepath.Join(base, "data"), an)
	n := len(g.ProtectedDirs)
	realDir, link := filepath.Join(base, "真身"), filepath.Join(base, "联接")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := junction.Create(link, realDir); err != nil {
		t.Skipf("建不了联接：%v", err)
	}
	g.Protect(link, link, realDir)
	lower := map[string]int{}
	for _, d := range g.ProtectedDirs {
		lower[strings.ToLower(d)]++
	}
	if lower[strings.ToLower(link)] != 1 || lower[strings.ToLower(realDir)] != 1 || len(g.ProtectedDirs) != n+2 {
		t.Fatalf("联接和它的真实路径各一份、不重复：%v", g.ProtectedDirs)
	}
}
