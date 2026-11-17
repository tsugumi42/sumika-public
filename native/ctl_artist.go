package main

// 画师表命令（sumika-ctl artist，规格-画师原图）接进桌面程序：表在这个进程里，导入、确认、移除改的就是链接插件正在用的那份。
// 只有 ArtistList 只读；导入的文件由程序自己读（路径校验在 internal/artists）。

import (
	"context"
	"errors"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.ArtistBackend = (*ctlBackend)(nil)

func artistErr(err error) error {
	if errors.Is(err, backend.ErrArtistInvalid) {
		return ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	return err
}

func (b *ctlBackend) ArtistList(ctx context.Context, platform, status string, n int) (ctlproto.ArtistListResult, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.ArtistListResult{}, err
	}
	out, err := r.ArtistList(ctx, platform, status, n)
	if err != nil {
		return ctlproto.ArtistListResult{}, artistErr(err)
	}
	res := ctlproto.ArtistListResult{Platform: platform, Status: status, Counts: out.Counts, Rows: []ctlproto.ArtistRow{}}
	for _, row := range out.Rows {
		res.Rows = append(res.Rows, ctlproto.ArtistRow{ID: row.ID, Handle: row.Handle, Name: row.Name, Status: row.Status, Source: row.Source, Score: row.Score, Clues: row.Clues, At: row.AtMS})
	}
	return res, nil
}

func artistChange(platform string, c backend.ArtistChangeOut) ctlproto.ArtistChange {
	return ctlproto.ArtistChange{Platform: platform, Batch: c.Batch, Added: c.Added, Upgraded: c.Upgraded, Already: c.Already, Declined: c.Declined, AlreadyDeclined: c.AlreadyDeclined, Skipped: c.Skipped, Malformed: c.Malformed,
		Restored: c.Restored, Deleted: c.Deleted, Confirmed: c.Confirmed, Removed: c.Removed}
}

func (b *ctlBackend) ArtistImport(ctx context.Context, platform, path string) (ctlproto.ArtistChange, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.ArtistChange{}, err
	}
	out, err := r.ArtistImport(ctx, platform, path)
	return artistChange(platform, out), artistErr(err)
}

func (b *ctlBackend) ArtistUndo(ctx context.Context, platform, batch string) (ctlproto.ArtistChange, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.ArtistChange{}, err
	}
	out, err := r.ArtistUndo(ctx, platform, batch)
	return artistChange(platform, out), artistErr(err)
}

func (b *ctlBackend) ArtistConfirm(ctx context.Context, platform string, ids []string) (ctlproto.ArtistChange, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.ArtistChange{}, err
	}
	out, err := r.ArtistConfirm(ctx, platform, ids)
	return artistChange(platform, out), artistErr(err)
}

func (b *ctlBackend) ArtistRemove(ctx context.Context, platform string, ids []string) (ctlproto.ArtistChange, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.ArtistChange{}, err
	}
	out, err := r.ArtistRemove(ctx, platform, ids)
	return artistChange(platform, out), artistErr(err)
}
