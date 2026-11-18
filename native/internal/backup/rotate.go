package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 轮换的留多少（规格-数据目录与备份 10.3）
const (
	dailyDays      = 7                         // 最近 7 天每天留最新一份
	weeklyWeeks    = 4                         // 再往前 4 周每周留最新一份（日历周）
	preChangeKeep  = 30 * 24 * time.Hour       // 改动前的留 30 天
	junkKeep       = 24 * time.Hour            // 坏的和 .partial 留 1 天
	dailyWindowEnd = dailyDays + weeklyWeeks*7 // 第 0 天起算，超过这一天的每天备份不留（最新一份除外）
)

// Decision 是轮换对一份备份的决定
type Decision struct {
	Entry  Entry
	Delete bool
	Why    string
}

// dayIndex：t 那天离 now 那天几天（本机日历日；今天是 0，昨天是 1，比今天还晚是负数）。夏令时那天不是 24 小时，四舍五入
func dayIndex(now, t time.Time) int {
	loc := now.Location()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	t = t.In(loc)
	ty, tm, td := t.Date()
	tday := time.Date(ty, tm, td, 0, 0, 0, 0, loc)
	h := today.Sub(tday).Hours() / 24
	if h >= 0 {
		return int(h + 0.5)
	}
	return int(h - 0.5)
}

// weekKey 是本机日历周（周一起）的编号：把本机的年月日当成一个日期数天数，不受夏令时影响
func weekKey(now, t time.Time) int {
	y, m, d := t.In(now.Location()).Date()
	days := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400) // 1970-01-01 是周四，1970-01-05 是第一个周一
	return (days - 4) / 7
}

// PlanRotation 是纯函数：给定现在和备份目录里认得出来的备份（Scan 的结果），算出每份留还是删。
//   - 好的（ok）每天：最近 7 天（今天是第 0 天，按本机日历日）每天留最新一份；
//     第 7～34 天按本机日历周（周一起）分桶，每周只留这一周里最新的一份（它要是已经在最近 7 天里，更老的就不另留）；其余删；
//     最新的一份好的每天备份永远不删。周按日历周分，不按“离现在几天”滑动分：滑动的窗口每天都在换，上一天选中的代表第二天就不是最新的了，逐天轮换下去一份周备份也留不住；
//   - 改动前：留 30 天；手动：不删；
//   - 坏的（没通过校验）：只留最新的一份、1 天以后也删；.partial：1 天以后删；
//   - 时间比现在还晚的（时钟往回拨过）一律不动：不知道哪份才是真的新。
func PlanRotation(now time.Time, entries []Entry) []Decision {
	out := make([]Decision, len(entries))
	dayRep := map[int]int{}  // 第几天（0～6）→ 这一天里最新一份的下标
	weekRep := map[int]int{} // 日历周 → 这一周里最新一份的下标（只看第 0～34 天里的）
	newestGood := -1
	newestBad := -1 // 没通过校验的里最新的一份：只留它（给人看原因），更早的立刻删，免得每小时重试一次堆出一排
	var dailies []int
	for i, e := range entries {
		out[i] = Decision{Entry: e}
		m := e.Manifest
		if m.Time.After(now) {
			out[i].Why = "时间比现在晚（时钟往回拨过），不动"
			continue
		}
		if !e.Partial && m.Status == StatusBad && (newestBad < 0 || newer(e, entries[newestBad])) {
			newestBad = i
		}
		if e.Partial || m.Status != StatusOK {
			if now.Sub(m.Time) >= junkKeep {
				out[i].Delete, out[i].Why = true, "没做完或没过校验，超过 1 天"
			} else {
				out[i].Why = "没做完或没过校验，还不到 1 天"
			}
			continue
		}
		switch m.Reason {
		case ReasonManual:
			out[i].Why = "手动备份，不删"
		case ReasonPreChange:
			if now.Sub(m.Time) >= preChangeKeep {
				out[i].Delete, out[i].Why = true, "改动前备份，超过 30 天"
			} else {
				out[i].Why = "改动前备份，30 天内"
			}
		case ReasonDaily:
			dailies = append(dailies, i)
			if newestGood < 0 || newer(e, entries[newestGood]) {
				newestGood = i
			}
			idx := dayIndex(now, m.Time)
			if idx < dailyDays {
				if j, ok := dayRep[idx]; !ok || newer(e, entries[j]) {
					dayRep[idx] = i
				}
			}
			if idx < dailyWindowEnd {
				wk := weekKey(now, m.Time)
				if j, ok := weekRep[wk]; !ok || newer(e, entries[j]) {
					weekRep[wk] = i
				}
			}
		}
	}
	for _, i := range dailies {
		e := entries[i]
		idx := dayIndex(now, e.Manifest.Time)
		switch {
		case idx < dailyDays:
			if dayRep[idx] == i {
				out[i].Why = "最近 7 天，这一天里最新的一份"
			} else {
				out[i].Delete, out[i].Why = true, "这一天里有更新的一份"
			}
		case idx < dailyWindowEnd:
			if weekRep[weekKey(now, e.Manifest.Time)] == i {
				out[i].Why = "每周留的一份：这一周里最新的"
			} else {
				out[i].Delete, out[i].Why = true, "这一周里有更新的一份"
			}
		default:
			out[i].Delete, out[i].Why = true, "每天备份，超出最近 7 天加 4 周"
		}
	}
	for i, e := range entries {
		if !e.Partial && e.Manifest.Status == StatusBad && i != newestBad && !e.Manifest.Time.After(now) && !out[i].Delete {
			out[i].Delete, out[i].Why = true, "没通过校验的里有更新的一份，只留最新的"
		}
	}
	if newestGood >= 0 && out[newestGood].Delete {
		out[newestGood].Delete, out[newestGood].Why = false, "最新的一份好的每天备份，永远留着"
	}
	return out
}

func newer(a, b Entry) bool {
	if !a.Manifest.Time.Equal(b.Manifest.Time) {
		return a.Manifest.Time.After(b.Manifest.Time)
	}
	return a.Name > b.Name
}

// RotateResult 是一次轮换的结果
type RotateResult struct {
	Deleted []string
	Failed  []string
}

// Rotate 按 PlanRotation 删旧备份，只动 source 这个数据目录的；junkOnly 为真时只清坏的和 .partial（新备份没成功时用：没有新的好备份，不能拿“过期”删好的）。
// 只动 Scan 认得出来的（有本程序清单、不是链接、名字对得上）；删的时候不跟随里面的联接。
func Rotate(dir string, now time.Time, junkOnly bool, source string) (RotateResult, error) {
	return RotateCtx(context.Background(), dir, now, junkOnly, source)
}

// RotateCtx 同 Rotate，删很多文件时每一项都看一眼 ctx，被取消就停（已经删掉的不回头）
func RotateCtx(ctx context.Context, dir string, now time.Time, junkOnly bool, source string) (RotateResult, error) {
	all, err := Scan(dir)
	if err != nil {
		return RotateResult{}, err
	}
	var entries []Entry
	for _, e := range all {
		if e.Manifest.Source == source { // 别的数据目录写进来的备份不是我们的，不轮换
			entries = append(entries, e)
		}
	}
	var res RotateResult
	for _, d := range PlanRotation(now, entries) {
		if !d.Delete {
			continue
		}
		if junkOnly && !(d.Entry.Partial || d.Entry.Manifest.Status != StatusOK) {
			continue
		}
		if err := removeTreeCtx(ctx, d.Entry.Folder); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s：%v", d.Entry.Name, err))
			continue
		}
		res.Deleted = append(res.Deleted, d.Entry.Name)
	}
	return res, nil
}

// removeTree 删一个文件夹：自己一层层删，遇到链接（符号链接、联接）只删链接本身、不进去，目标里的东西不碰
func removeTree(path string) error { return removeTreeCtx(context.Background(), path) }

func removeTreeCtx(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if linkOnDisk(path) || !fi.IsDir() {
		return removeOne(path)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := removeTreeCtx(ctx, filepath.Join(path, e.Name())); err != nil {
			return err
		}
	}
	return removeOne(path)
}

// removeOne 删一个文件、空目录或链接本身（只读文件先去掉只读）
func removeOne(path string) error {
	err := os.Remove(path)
	if err == nil {
		return nil
	}
	if fi, e := os.Lstat(path); e == nil && !fi.IsDir() {
		_ = os.Chmod(path, 0o666)
		return os.Remove(path)
	}
	return err
}
