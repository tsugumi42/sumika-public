package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/identity"
)

// 编的卡，只写测到的那几行；卡目录用 t.TempDir()，不碰真实数据目录
func writeCard(t *testing.T, id, yml string) string {
	t.Helper()
	dir := t.TempDir()
	card := filepath.Join(dir, id, "card")
	if err := os.MkdirAll(card, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ownerName 是可选的称呼：写了读出来，没写是空、用的时候叫"用户"
func TestOwnerNameAndCall(t *testing.T) {
	t.Parallel()
	sixteen := strings.Repeat("称", 16)
	for _, c := range []struct{ name, yml, wantName, wantCall string }{
		{"没写", "name: 铃\n", "", "用户"},
		{"写了", "name: 铃\nownerName: 主人\n", "主人", "主人"},
		{"十六个字", "name: 铃\nownerName: " + sixteen + "\n", sixteen, sixteen},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeCard(t, "suzu", c.yml)
			got, err := Load(dir, "suzu", "m1")
			if err != nil {
				t.Fatal(err)
			}
			if got.OwnerName != c.wantName || got.OwnerCall() != c.wantCall {
				t.Errorf("OwnerName %q、OwnerCall %q，要 %q / %q", got.OwnerName, got.OwnerCall(), c.wantName, c.wantCall)
			}
			if got := OwnerCallFor(dir, "suzu"); got != c.wantCall {
				t.Errorf("OwnerCallFor %q，要 %q", got, c.wantCall)
			}
		})
	}
	if got := OwnerCallFor(t.TempDir(), "没有这张卡"); got != "用户" {
		t.Errorf("读不到卡时 %q，要 用户", got)
	}
}

// ownerName 不合规：空、null、不是字、带换行/括号/冒号/方括号/控制字符、首尾空白、纯空白、超过 16 个字，
// 读卡都报错并指出 ownerName
func TestOwnerNameInvalid(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, yml string }{
		{"空", "name: 铃\nownerName: \"\"\n"},
		{"null", "name: 铃\nownerName: null\n"},
		{"不是字", "name: 铃\nownerName: 12\n"},
		{"换行", "name: 铃\nownerName: \"主\\n人\"\n"},
		{"回车的换行", "name: 铃\nownerName: \"主\\r人\"\n"},
		{"左花括号", "name: 铃\nownerName: \"{主人}\"\n"},
		{"右花括号", "name: 铃\nownerName: \"主人}\"\n"},
		{"半角括号", "name: 铃\nownerName: \"主人(笑)\"\n"},
		{"全角括号", "name: 铃\nownerName: \"主人（笑）\"\n"},
		{"半角冒号", "name: 铃\nownerName: \"主人:x\"\n"},
		{"全角冒号", "name: 铃\nownerName: \"主人：x\"\n"},
		{"半角方括号", "name: 铃\nownerName: \"主人[笑]\"\n"},
		{"全角方括号", "name: 铃\nownerName: \"主人［笑］\"\n"},
		{"前面有空白", "name: 铃\nownerName: \" 主人\"\n"},
		{"后面有空白", "name: 铃\nownerName: \"主人 \"\n"},
		{"纯空白", "name: 铃\nownerName: \" \"\n"},
		{"控制字符", "name: 铃\nownerName: \"主人\\u0001\"\n"},
		{"删除字符", "name: 铃\nownerName: \"主人\\u007f\"\n"},
		{"行分隔符", "name: 铃\nownerName: \"主人\\u2028\"\n"},
		{"段分隔符", "name: 铃\nownerName: \"主人\\u2029\"\n"},
		{"十七个字", "name: 铃\nownerName: " + strings.Repeat("称", 17) + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeCard(t, "suzu", c.yml)
			_, err := Load(dir, "suzu", "m1")
			if err == nil || !strings.Contains(err.Error(), "ownerName") {
				t.Fatalf("要报 ownerName 的错误，得到 %v", err)
			}
			if _, err := Name(dir, "suzu"); err == nil || !strings.Contains(err.Error(), "ownerName") {
				t.Fatalf("Name 也要报 ownerName 的错误，得到 %v", err)
			}
		})
	}
}

// ownerName 不能占用消息头、聊天记录里的固定身份词：卡上写"群友"，所有群友和所有者的行都是 名(群友)，分不清谁是谁
func TestOwnerNameReserved(t *testing.T) {
	t.Parallel()
	for _, word := range identity.Reserved {
		t.Run(word, func(t *testing.T) {
			t.Parallel()
			dir := writeCard(t, "suzu", "name: 铃\nownerName: "+word+"\n")
			_, err := Load(dir, "suzu", "m1")
			if err == nil || !strings.Contains(err.Error(), "ownerName") {
				t.Fatalf("ownerName: %s 要报 ownerName 的错误，得到 %v", word, err)
			}
			if got := OwnerCallFor(dir, "suzu"); got != "用户" {
				t.Errorf("读不出来时叫 %q，要 用户", got)
			}
		})
	}
}
