package backup

import (
	"os"
	"path/filepath"
	"time"
)

// RotationRules 是写给人看的轮换规则（sumika-ctl backup list 的总览里显示；规则本身在 rotate.go 的 PlanRotation）
const RotationRules = "每天备份：最近 7 天每天留最新一份，再往前 4 个日历周（周一起）每周留最新一份，其余删，最新的一份好的永远留着；" +
	"改动前备份留 30 天；手动备份不自动删；没做完的和没过校验的留 1 天；只在新备份校验通过后才轮换，只动带本程序清单的文件夹"

// Overview 是备份目录的总览：共几份、按原因各几份、实际占用（硬链接只算一次）、规则、位置和所在盘剩余空间
type Overview struct {
	Dir           string
	DirError      string // 备份目录算不出来（比如指到了数据目录里面）时写原因，其余字段是零
	Total         int    // 认得出来的备份份数（含没做完和没过校验的）
	Daily         int
	Manual        int
	PreChange     int
	Bad           int // 没过校验的
	Partial       int // 没做完的
	Foreign       int // 别的数据目录写进这个备份目录的备份份数（不参与轮换、不算在上面）
	UsedBytes     int64
	UsedAt        time.Time // UsedBytes 是哪次备份结束时算的；估算时是零
	UsedEstimated bool      // 没有算好的占用（旧备份目录、小文件被删），按各份清单估算：自己占的（总大小减共用的）加起来，不去逐个文件取身份
	FreeBytes     int64     // 备份目录所在盘的剩余空间；FreeKnown 为假时不知道
	FreeKnown     bool
	AlertGB       float64
	DailyOn       bool   // backup.daily
	Media         string // backup.dailyMedia
	Rules         string
	DrillOn       bool       // 每周自动抽验开着（跟着 backup.daily）
	Drill         DrillState // 抽验的状态：最近一次和最近一次通过
}

// Overview 是备份目录的总览，同时返回属于这个数据目录的备份（新的在前），backup list 一次扫描用。
// 只读各份清单的头（大小、统计早就记在里面）和每次备份结束时算好的占用（.usage.json），不遍历备份里的文件、不逐个取文件身份：
// 带全部媒体时清单和文件都很多，现算要几十秒，超过命令行等回复的时限。
func (s *Service) Overview() (Overview, []Entry) {
	o := Overview{AlertGB: s.opt.AlertGB, DailyOn: s.opt.Daily, Media: s.opt.Media, Rules: RotationRules}
	dir, err := s.Dir()
	if err != nil {
		o.DirError = err.Error()
		return o, nil
	}
	o.Dir = dir
	s.loadDrills(dir)
	o.DrillOn, o.Drill = s.opt.Drill, s.DrillStatus()
	all, _ := Scan(dir)
	entries := s.ours(all)
	o.Foreign = len(all) - len(entries)
	for _, e := range entries {
		o.Total++
		switch {
		case e.Partial || e.Manifest.Status == StatusPartial:
			o.Partial++
		case e.Manifest.Status == StatusBad:
			o.Bad++
		}
		switch e.Manifest.Reason {
		case ReasonDaily:
			o.Daily++
		case ReasonManual:
			o.Manual++
		case ReasonPreChange:
			o.PreChange++
		}
	}
	if u, ok := readUsage(dir, s.source()); ok {
		o.UsedBytes, o.UsedAt = u.Bytes, u.At
	} else {
		o.UsedEstimated = true
		for _, e := range entries {
			o.UsedBytes += e.Manifest.Bytes - e.Manifest.SharedBytes
		}
	}
	o.FreeBytes, o.FreeKnown = freeSpace(nearestExisting(dir))
	return o, entries
}

func nearestExisting(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
