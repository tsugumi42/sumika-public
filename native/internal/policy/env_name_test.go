package policy

import "testing"

// grep 过滤搜索结果和受保护路径判断共用 IsEnvFileName：.env 和 .env.任意后缀，不分大小写；.envrc 之类不算
func TestIsEnvFileName(t *testing.T) {
	for name, want := range map[string]bool{
		".env": true, ".ENV": true, ".Env": true, ".env.local": true, ".env.Production": true, ".env.a.b": true,
		".envrc": false, "env": false, "a.env": false, ".environment": false, ".env.": false, "": false,
	} {
		if got := IsEnvFileName(name); got != want {
			t.Errorf("IsEnvFileName(%q) = %v，应该是 %v", name, got, want)
		}
	}
}
