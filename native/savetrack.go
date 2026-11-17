package main

// saveTrack 记一类防抖保存的账（只在界面线程里用）：写盘由 deskSaves 按顺序在后台做，结果再交回界面线程对账。
// 三件事：每次写盘领一个序号，晚到的旧结果（序号不是最新的）不能改账；写失败要退回"磁盘上现在是什么"，
// 不能一直当成已经存上；失败过的同一份值不原样重试（磁盘一直写不进去时每 500 毫秒重写一次没有意义），用户再改了才试。
type saveTrack[T comparable] struct {
	seq       uint64 // 最近一次排队的写盘序号
	disk      T      // 最近一次确认写进磁盘的（或读到的）值
	failed    T      // 最近一次写失败的值
	hasFailed bool
}

// begin 排一次写盘，返回它的序号。
func (t *saveTrack[T]) begin() uint64 {
	t.seq++
	t.hasFailed = false
	return t.seq
}

// finish 收一次写盘的结果。返回 true 表示这是最新一次写、而且失败了：调用方把"已保存"退回 t.disk 并提示。
// 旧的写盘失败（后面已经排了新的）返回 false：新那次才说得清磁盘上是什么。
func (t *saveTrack[T]) finish(seq uint64, v T, err error) bool {
	if err == nil {
		t.disk = v
		return false
	}
	if seq != t.seq {
		return false
	}
	t.failed, t.hasFailed = v, true
	return true
}

// skip 说 cur 刚写失败过、之后没人再改，不原样重试。
func (t *saveTrack[T]) skip(cur T) bool { return t.hasFailed && cur == t.failed }

// rearm 在用户又动了这项设置时清掉"失败过"：改回失败的那个值也要再试一次。
func (t *saveTrack[T]) rearm() { t.hasFailed = false }

// adopt 记下读盘读到的值：磁盘上就是它。
func (t *saveTrack[T]) adopt(v T) { t.disk = v }
