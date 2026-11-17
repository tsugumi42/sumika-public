package mediastore

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// 新增字节回调：真正落下新文件才通知；同哈希已存在、写失败都不通知；撤销后不再通知
func TestOnNewFileNotifiesOnlyRealWrites(t *testing.T) {
	data := t.TempDir()
	var mu sync.Mutex
	var got []NewFile
	cancel := OnNewFile(func(n NewFile) {
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
	})
	defer cancel()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }

	if _, _, err := Put(data, "qq", strings.NewReader("hello"), 1<<20); err != nil {
		t.Fatal(err)
	}
	if count() != 1 || got[0].Bytes != 5 || got[0].Platform != "qq" || got[0].Data != data {
		t.Fatalf("新文件要通知一次：%+v", got)
	}
	if _, _, err := Put(data, "qq", strings.NewReader("hello"), 1<<20); err != nil { // 同哈希：已经在了
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("同哈希不该再通知：%+v", got)
	}
	if _, _, err := Put(data, "local", strings.NewReader("hello"), 1<<20); err != nil { // 别的平台是另一份文件
		t.Fatal(err)
	}
	if count() != 2 || got[1].Platform != "local" {
		t.Fatalf("另一个平台的同内容是新文件：%+v", got)
	}
	if _, _, err := Put(data, "qq", strings.NewReader("this is too big"), 3); !errors.Is(err, ErrMediaTooBig) {
		t.Fatalf("超限要失败：%v", err)
	}
	if _, _, err := Put(data, "qq", strings.NewReader(""), 10); err == nil {
		t.Fatal("空内容要失败")
	}
	if count() != 2 {
		t.Fatalf("失败的写入不该通知：%+v", got)
	}
	cancel()
	if _, _, err := Put(data, "qq", strings.NewReader("after"), 1<<20); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatalf("撤销后不该再通知：%+v", got)
	}
}
