package channel

import "testing"

// 名册允许零个账号：只开本地时没有 QQ 账号，闸门、出口、入库一站、回合照样要能建。
// 只拦“填了一半”和“同一个号绑两个角色”，不要求至少有一条连接。
func TestRosterAllowsZeroAccounts(t *testing.T) {
	if err := (Roster{}).validate(); err != nil {
		t.Fatalf("零个账号的名册该合法：%v", err)
	}
	if err := (Roster{Owners: map[string]string{"qq": "200"}}).validate(); err != nil {
		t.Fatalf("只有主人、没有账号的名册该合法：%v", err)
	}
}
