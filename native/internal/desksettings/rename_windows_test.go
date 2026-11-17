package desksettings

import (
	"os"
	"testing"
	"time"
)

// Windows 上目标文件正被别的程序用默认的共享方式打开时（杀毒软件扫描、编辑器、备份工具），改名盖过去会得到"拒绝访问"。
// 对方很快放手的话，托盘勾选或 auto-receive on 不该失败；一直不放手的话，错误要报出来，旧文件完好，不留临时文件

func TestSetRetriesWhileTheFileIsBrieflyOpenElsewhere(t *testing.T) {
	s := New(newDir(t))
	if err := s.SetAutoReceive(false); err != nil { // 先有一个文件
		t.Fatal(err)
	}
	reader, err := os.Open(s.File()) // 默认共享方式：别人想改名盖过它会被拒
	if err != nil {
		t.Fatal(err)
	}
	// 先确认这个手法真的会挡住改名，不然下面的测试什么也没验证
	probe := s.File() + ".probe"
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, s.File()); err == nil {
		t.Fatal("夹具没有挡住改名（对方打开着文件时 Rename 应失败）")
	}
	os.Remove(probe)

	go func() {
		time.Sleep(300 * time.Millisecond)
		reader.Close()
	}()
	if err := s.SetAutoReceive(true); err != nil {
		t.Fatalf("对方 0.3 秒后放手，应该重试到成功：%v", err)
	}
	if on, err := s.AutoReceive(); !on || err != nil {
		t.Fatalf("文件里应是开：%v %v", on, err)
	}
	if left := leftovers(t, s); len(left) != 0 {
		t.Fatalf("不该留临时文件：%v", left)
	}
}

func TestSetGivesUpWhenTheFileStaysOpen(t *testing.T) {
	old := renameRetryFor
	renameRetryFor = 150 * time.Millisecond
	t.Cleanup(func() { renameRetryFor = old })
	s := New(newDir(t))
	if err := s.SetAutoReceive(false); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(s.File())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	began := time.Now()
	if err := s.SetAutoReceive(true); err == nil {
		t.Fatal("对方一直不放手，应该报错")
	}
	if d := time.Since(began); d < 100*time.Millisecond {
		t.Errorf("应该重试到时限才放弃，只用了 %v", d)
	}
	reader.Close()
	if on, err := s.AutoReceive(); on || err != nil {
		t.Fatalf("失败以后旧文件要完好（仍是关）：%v %v", on, err)
	}
	if left := leftovers(t, s); len(left) != 0 {
		t.Fatalf("失败不该留临时文件：%v", left)
	}
}
