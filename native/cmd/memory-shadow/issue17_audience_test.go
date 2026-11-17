package main

import "testing"

// 听众是工具与召回共同的身份边界；预期手写，不能从同一个转换函数生成。
func TestIssue17AudienceContracts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		conv, actor           string
		privateOK, ownerAsked bool
	}{
		{"console:w6b-lan", "owner", true, true},
		{"console:group:w6b-home", "owner", true, true},
		{"qq:private:101:909", "owner", true, true},
		{"qq:private:101:606", "qq:606", false, false},
		{"qq:group:707", "owner", false, true},
		{"qq:group:707", "qq:606", false, false},
	}
	for _, c := range cases {
		a := issue17Audience(c.conv, c.actor)
		if a.PrivateOK != c.privateOK || a.OwnerAsked != c.ownerAsked {
			t.Errorf("%s actor=%s：PrivateOK=%t OwnerAsked=%t，期望 %t/%t", c.conv, c.actor, a.PrivateOK, a.OwnerAsked, c.privateOK, c.ownerAsked)
		}
	}
}
