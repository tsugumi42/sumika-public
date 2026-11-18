package linkcard

import (
	"errors"
	"testing"
)

// 同一份字体只交给 mygo 一次；换字族、换内容照常注册；失败不记，下次再试
func TestOnceRegistrarDedupes(t *testing.T) {
	calls := 0
	fail := false
	reg := onceRegistrar(func([]byte, string) error {
		calls++
		if fail {
			return errors.New("编的注册失败")
		}
		return nil
	})
	a, b := []byte("字体甲的内容"), []byte("字体乙的内容")
	for range 5 {
		if err := reg(a, "编造字族"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("同一份字体注册了 %d 次，应为 1", calls)
	}
	_ = reg(a, "另一个字族")
	_ = reg(b, "编造字族")
	if calls != 3 {
		t.Fatalf("换字族、换内容应各注册一次，现在共 %d 次", calls)
	}
	fail = true
	c := []byte("字体丙")
	if err := reg(c, "编造字族"); err == nil {
		t.Fatal("注册失败应报错")
	}
	fail = false
	if err := reg(c, "编造字族"); err != nil || calls != 5 {
		t.Fatalf("失败不该记下来：err %v，共 %d 次", err, calls)
	}
}
