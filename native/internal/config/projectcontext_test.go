package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectContextConfig(t *testing.T) {
	root := t.TempDir()
	real := filepath.ToSlash(root)
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"默认", "{}", false},
		{"块里不写开关", "projectContext:\n  userContext: false\n", false},
		{"启用", "projectContext:\n  enabled: true\n  userContext: true\n  trustedRoots: [\"" + real + "\"]\n", false},
		{"未知键", "projectContext:\n  trustEverything: true\n", true},
		{"坏开关", "projectContext:\n  enabled: potato\n", true},
		{"相对根", "projectContext:\n  trustedRoots: [relative]\n", true},
		{"根不存在", "projectContext:\n  trustedRoots: [\"" + real + "/missing\"]\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "sumika.yml"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(root)
			if tc.bad {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "projectcontext") {
					t.Fatalf("坏配置没有定位诊断：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "默认" || tc.name == "块里不写开关" {
				// 默认开着，但没有信任根：信任靠问一次记住，不预先信任任何目录
				if !c.ProjectContext.Enabled || c.ProjectContext.UserContext || len(c.ProjectContext.TrustedRoots) != 0 {
					t.Fatalf("默认应开启、不预先信任任何目录：%+v", c.ProjectContext)
				}
			} else if !c.ProjectContext.Enabled || !c.ProjectContext.UserContext || len(c.ProjectContext.TrustedRoots) != 1 {
				t.Fatalf("配置没读进来：%+v", c.ProjectContext)
			}
		})
	}
}

func TestProjectContextConfigPinsIdentity(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "trusted")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	var c ProjectContext
	if err := c.UnmarshalYAML([]byte("trustedRoots: [\"" + filepath.ToSlash(root) + "\"]")); err != nil {
		t.Fatal(err)
	}
	if err := c.RootChecks[0](); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.RootChecks[0](); err == nil || !strings.Contains(err.Error(), "身份已改变") {
		t.Fatalf("替换的根未被拒绝：%v", err)
	}
}
