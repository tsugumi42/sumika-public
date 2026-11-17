package app

import (
	"context"
	"errors"

	"sumika-native/internal/artists"
)

// 画师表的命令行入口（sumika-ctl artist ...，规格-画师原图 三点二）。表在这个进程里：导入、确认、移除改的就是链接插件正在用的那份，立刻生效。
// 只有 ArtistList 只读；别的三个会改 go/artists/<平台>.json（不碰 local.db 和别的状态文件）。

// ErrArtistInvalid 是请求本身不对（平台没做、ID 格式不对、导入文件不对），不是后台出了问题。
var ErrArtistInvalid = errors.New("画师表的参数不合法")

// artistInvalidErr 是 ErrArtistInvalid 的一种：文字就是具体原因，不带前缀，给用户直接看
type artistInvalidErr struct{ msg string }

func (e artistInvalidErr) Error() string        { return e.msg }
func (e artistInvalidErr) Is(target error) bool { return target == ErrArtistInvalid }
func invalidArtist(err error) error             { return artistInvalidErr{err.Error()} }

// artistBook 是这个 App 的画师表（懒建，ctl 命令和链接插件共用一份）。
func (a *App) artistBook() *artists.Book {
	a.artistsOnce.Do(func() {
		a.artistsBook = artists.NewBook(a.paths.ArtistsDir(), artists.Options{Now: a.now, Logf: a.logf})
	})
	return a.artistsBook
}

// ArtistRow 是 ArtistList 的一行
type ArtistRow struct {
	ID, Handle, Name, Status, Source string
	Score                            float64
	Clues                            []string
	AtMS                             int64
}

// ArtistListOut 是 ArtistList 的结果：Counts 是整张表各状态的行数
type ArtistListOut struct {
	Counts map[string]int
	Rows   []ArtistRow
}

// ArtistChangeOut 是导入、撤回、确认、移除的结果（计数）
type ArtistChangeOut struct {
	Batch                                        string
	Added, Upgraded, Already, Skipped, Malformed int
	Declined, AlreadyDeclined                    int
	Restored, Deleted, Confirmed, Removed        int
}

func (r *DesktopReader) artistStore(platform string) (*artists.Store, error) {
	st, err := r.desk.app.artistBook().Platform(platform)
	if err != nil {
		return nil, invalidArtist(err)
	}
	return st, nil
}

// artistErr 把表的错分成“请求不对”和“后台出错”
func artistErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, artists.ErrBadID), errors.Is(err, artists.ErrPlatform):
		return invalidArtist(err)
	}
	return err
}

func (r *DesktopReader) ArtistList(ctx context.Context, platform, status string, n int) (ArtistListOut, error) {
	st, err := r.artistStore(platform)
	if err != nil {
		return ArtistListOut{}, err
	}
	out := ArtistListOut{Counts: st.Counts()}
	for _, row := range st.List(status, n) {
		out.Rows = append(out.Rows, ArtistRow{ID: row.ID, Handle: row.Handle, Name: row.Name, Status: row.Status, Source: row.Source,
			Score: row.Score, Clues: row.Clues, AtMS: row.At.UnixMilli()})
	}
	return out, nil
}

func (r *DesktopReader) ArtistImport(ctx context.Context, platform, path string) (ArtistChangeOut, error) {
	st, err := r.artistStore(platform)
	if err != nil {
		return ArtistChangeOut{}, err
	}
	data, err := artists.ReadImportFile(path)
	if err != nil {
		return ArtistChangeOut{}, invalidArtist(err)
	}
	rep, err := st.Import(data)
	if err != nil {
		return ArtistChangeOut{}, invalidArtist(err)
	}
	r.desk.app.logf("画师表：导入，新增 %d、改为生效 %d、本来就生效 %d、标为不是 %d、本来就是不是 %d、跳过 %d、格式不对 %d", rep.Added, rep.Upgraded, rep.Already, rep.Declined, rep.AlreadyDeclined, rep.Skipped, rep.Malformed)
	return ArtistChangeOut{Batch: rep.Batch, Added: rep.Added, Upgraded: rep.Upgraded, Already: rep.Already, Declined: rep.Declined, AlreadyDeclined: rep.AlreadyDeclined, Skipped: rep.Skipped, Malformed: rep.Malformed}, nil
}

func (r *DesktopReader) ArtistUndo(ctx context.Context, platform, batch string) (ArtistChangeOut, error) {
	st, err := r.artistStore(platform)
	if err != nil {
		return ArtistChangeOut{}, err
	}
	rep, err := st.Undo(batch)
	if err != nil {
		return ArtistChangeOut{}, invalidArtist(err)
	}
	r.desk.app.logf("画师表：撤回一个导入批次，恢复 %d、删除 %d", rep.Restored, rep.Deleted)
	return ArtistChangeOut{Restored: rep.Restored, Deleted: rep.Deleted}, nil
}

func (r *DesktopReader) ArtistConfirm(ctx context.Context, platform string, ids []string) (ArtistChangeOut, error) {
	st, err := r.artistStore(platform)
	if err != nil {
		return ArtistChangeOut{}, err
	}
	rep, err := st.Confirm(ids)
	if err != nil {
		return ArtistChangeOut{}, artistErr(err)
	}
	r.desk.app.logf("画师表：确认 %d 个", rep.Confirmed)
	return ArtistChangeOut{Confirmed: rep.Confirmed, Already: rep.Already}, nil
}

func (r *DesktopReader) ArtistRemove(ctx context.Context, platform string, ids []string) (ArtistChangeOut, error) {
	st, err := r.artistStore(platform)
	if err != nil {
		return ArtistChangeOut{}, err
	}
	rep, err := st.Remove(ids)
	if err != nil {
		return ArtistChangeOut{}, artistErr(err)
	}
	r.desk.app.logf("画师表：移除 %d 个", rep.Removed)
	return ArtistChangeOut{Removed: rep.Removed, Already: rep.Already}, nil
}
