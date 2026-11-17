package app

// 平台库打不开时只停这个平台（规格-库版本与升级护栏 第 8 条）：App 把 Set 记下的原因交给宿主、自检和 db status。
// 这里是读取和措辞的唯一地方，别处不自己拼。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/dbguard"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// StoreUnavailable 是登记了、库却打不开的平台（平台名 → 原因）。没有时是空表。
func (a *App) StoreUnavailable() map[string]*dbguard.Unavailable {
	if a == nil || a.set == nil {
		return map[string]*dbguard.Unavailable{}
	}
	return a.set.Unavailable()
}

// StoreUnavailableLines 给宿主状态页：每个不可用平台一行，带解除办法。
// 例：qq 的库没打开：第 9 号迁移失败：…；库已换回迁移前；快照在 …（sumika-ctl db status 看详情）
func (a *App) StoreUnavailableLines() []string {
	m := a.StoreUnavailable()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		u := m[name]
		reason := u.Detail
		if reason == "" {
			reason = u.Reason
		}
		if u.Snapshot != "" {
			reason += "；快照在 " + u.Snapshot
		}
		out = append(out, fmt.Sprintf("%s 的库没打开：%s（sumika-ctl db status 看详情）", name, reason))
	}
	return out
}

// storeUnavailableSelfcheck 把 Set.Unavailable 交给 S14（自检包不 import store）。
func (a *App) storeUnavailableSelfcheck() []selfcheck.StoreUnavailable {
	m := a.StoreUnavailable()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]selfcheck.StoreUnavailable, 0, len(names))
	for _, name := range names {
		u := m[name]
		out = append(out, selfcheck.StoreUnavailable{
			Platform: name, Track: u.Track, Reason: u.Reason, Detail: u.Detail, Snapshot: u.Snapshot,
		})
	}
	return out
}

// storeUnavailableAllError 是所有平台的库都打不开时的错误：逐个写出平台名、固定原因词和一句详情，
// 不能盖成“没有能用的库”（规格第 8 条末）。调用方只在 Platforms() 为空时用。
func storeUnavailableAllError(set *store.Set) error {
	m := set.Unavailable()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("所有平台的库都没打开")
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		u := m[name]
		detail := u.Detail
		if detail == "" {
			detail = u.Reason
		}
		parts = append(parts, fmt.Sprintf("%s 的库没打开（%s）：%s", name, u.Reason, detail))
	}
	return fmt.Errorf("所有平台的库都打不开：%s", strings.Join(parts, "；"))
}

// DBStatus 是 sumika-ctl db status：每个库（trace、每个平台）打开时记下的版本、最低可读、程序的最高版本、
// 状态和最近一次快照。数据全是 Set 在打开时记下的，这里不重新开库（规格第 8 条）。
func (a *App) DBStatus(ctx context.Context) (ctlproto.DBStatus, error) {
	if err := ctx.Err(); err != nil {
		return ctlproto.DBStatus{}, err
	}
	if a == nil || a.set == nil {
		return ctlproto.DBStatus{}, ctlproto.Errorf(ctlproto.CodeFailed, "库还没打开")
	}
	rows := a.set.DBStatus()
	out := ctlproto.DBStatus{Databases: make([]ctlproto.DBDatabase, 0, len(rows))}
	for _, row := range rows {
		db := ctlproto.DBDatabase{
			Name: row.Name, Path: row.Path, Status: row.Status,
			Reason: row.Reason, Detail: row.Detail, Snapshot: row.Snapshot,
			Tracks: make([]ctlproto.DBTrack, 0, len(row.Tracks)),
		}
		for _, t := range row.Tracks {
			db.Tracks = append(db.Tracks, ctlproto.DBTrack{Track: t.Track, Version: t.Version, MinReader: t.MinReader, Latest: t.Latest})
		}
		out.Databases = append(out.Databases, db)
	}
	return out, nil
}
