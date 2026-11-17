//go:build windows

package storagestat

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/paths"
)

func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("建不了联接：%v %s", err, out)
	}
}

// 验收①：目录里的联接不跟进去
func TestJunctionInsideNotFollowed(t *testing.T) {
	p := newData(t)
	mk(t, p.Root, "dev/real", 10)
	outside := t.TempDir()
	mk(t, outside, "big", 5000)
	junction(t, filepath.Join(p.DevDir(), "link"), outside)
	s := New(p, Options{})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.Storage(context.Background())
	dev := cat(t, rep, ctlproto.StoreDev)
	if dev.Bytes != 10 || dev.Links != 1 {
		t.Errorf("联接里的 5000 字节不该算进来：%+v", dev)
	}
}

// 验收①：数据目录本身是联接时能解
func TestDataRootIsJunction(t *testing.T) {
	real := newData(t)
	mk(t, real.Root, "logs/a.log", 33)
	mk(t, real.Root, "go/media/qq/ab/h", 44)
	link := filepath.Join(t.TempDir(), "data-link")
	junction(t, link, real.Root)
	p, err := paths.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	s := New(p, Options{})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.Storage(context.Background())
	if c := cat(t, rep, ctlproto.StoreLogs); c.Bytes != 33 || c.Links != 0 {
		t.Errorf("通过联接要量得到真实内容：%+v", c)
	}
	if c := cat(t, rep, ctlproto.StoreMedia); c.Bytes != 44 {
		t.Errorf("媒体：%+v", c)
	}
	if rep.Total != dirTotal(t, real.Root) {
		t.Errorf("总数 %d，实际 %d", rep.Total, dirTotal(t, real.Root))
	}
}
