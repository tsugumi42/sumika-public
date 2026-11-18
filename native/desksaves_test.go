package main

import (
	"slices"
	"testing"
	"time"
)

// 第一个写还卡着，后面排队的不能抢先：连点两下，文件里留下的要是后点的那个。
func TestDeskSavesInOrder(t *testing.T) {
	t.Parallel()
	var q deskSaves
	release := make(chan struct{})
	entered := make(chan struct{})
	var got []int
	q.add(func() {
		close(entered)
		<-release
		got = append(got, 1)
	})
	<-entered
	for i := 2; i <= 5; i++ {
		q.add(func() { got = append(got, i) })
	}
	close(release)
	q.wait()
	if !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("写的顺序不对：%v", got)
	}
}

// wait 等到正在写的写完，什么都没排时马上返回。
func TestDeskSavesWait(t *testing.T) {
	t.Parallel()
	var q deskSaves
	q.wait()
	release := make(chan struct{})
	entered := make(chan struct{})
	finished := false
	q.add(func() {
		close(entered)
		<-release
		finished = true
	})
	<-entered
	waited := make(chan struct{})
	go func() { q.wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("还在写就返回了")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-waited:
	case <-time.After(time.Minute):
		t.Fatal("写完了 wait 还不返回")
	}
	if !finished {
		t.Fatal("wait 返回时还没写完")
	}
	q.add(func() {}) // 排空以后还能接着用
	q.wait()
}
