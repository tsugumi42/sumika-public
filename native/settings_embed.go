package main

// 设置页“记忆和学习”缺嵌入模型时的入口（规格-嵌入不依赖外部程序 第 5 条）：
// 嵌入后端那一行下面写它在找的完整文件路径，给“打开下载页”（登记表有地址才显示）和“指定模型目录”两个按钮。
// 缺模型状态只有设置注册表读得到（api.SettingsView.EmbedMissingModel）；写用同一份快照的版本号，
// 成功后在这一节提示“重新开始接收后生效”，写失败写明原因、值不变、下一帧重读。

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

const (
	embedBackendKey    = "embed.backend"
	embedModelsRootKey = "embed.modelsRoot"
	embedMissingPrefix = "找不到模型文件："
	embedRestartNote   = "重新开始接收后生效"
)

// pickEmbedModelsFolder 弹系统的选文件夹窗口；空路径 = 用户取消了。测试里换成替身。
var pickEmbedModelsFolder = func(parent *mygo.Window, title, start string) (string, error) {
	dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: title, DefaultPath: start, Directory: true})
	if err != nil || len(dirs) == 0 {
		return "", err
	}
	return dirs[0], nil
}

type embedModelUI struct {
	gen     uint64
	asked   bool
	busy    bool
	version api.SettingsVersion
	missing *api.EmbedMissingModel
	loadErr string
	done    string
	err     string

	// 路径一行只量一次：同一条路径、同样窗宽和字号不重排（200 字符每帧二分查找太浪费）
	lineFor  string
	lineW    float32
	lineSize float32
	line     string
}

// embedMissingBlock 画缺模型那一节。后台没有设置注册表（演示、旧数据源）或还没读到时不画东西。
func (a *app) embedMissingBlock(c *ui.Context) {
	reg, ok := a.reader.(api.SettingsRegistry)
	if !ok {
		return
	}
	st := &a.embedUI
	if st.gen != a.srcGen {
		*st = embedModelUI{gen: a.srcGen}
	}
	if !st.asked && !st.busy {
		st.asked, st.busy = true, true
		a.loadEmbedMissing(reg)
	}
	m := st.missing
	if m == nil && st.loadErr == "" && st.done == "" && st.err == "" {
		return
	}
	k := a.settingsKit()
	gap, size := float32(4), float32(12)
	if k != nil {
		gap, size = k.G.SpaceSmall, k.G.MetaSize
	}
	ui.Column(c.Key("embed-missing")).FillWidth().Gap(gap).Children(func() {
		if m != nil {
			maxW := a.settingsTextWidth(c, k)
			line := a.embedMissingLine(c, m.Path, maxW, size)
			ink := c.Theme().TextMuted
			if k != nil {
				ink = k.P.Muted
			}
			e := ui.Text(c.Key("embed-missing-path"), line).FontSize(size).SingleLine().TextColor(ink).MaxWidth(maxW)
			if line != embedMissingPrefix+m.Path {
				e.Tooltip(m.Path)
			}
			ui.Row(c.Key("embed-missing-actions")).Gap(gap).AlignItems(ui.Center).Children(func() {
				if m.DownloadURL != "" {
					a.settingsButton(c, "embed-download", "打开下载页", false, false, func() { a.openLink(c, m.DownloadURL) })
				}
				a.settingsButton(c, "embed-models-root", "指定模型目录", false, st.busy, func() { a.pickEmbedModelsRoot(m) })
			})
		}
		if st.loadErr != "" {
			a.embedMissingNote(c, "读不到模型状态："+st.loadErr, true)
		}
		if st.err != "" {
			a.embedMissingNote(c, st.err, true)
		}
		if st.done != "" {
			a.embedMissingNote(c, st.done, false)
		}
	})
}

// embedMissingNote 是这一节下面的一行说明：失败用危险色，成功用弱一档。
func (a *app) embedMissingNote(c *ui.Context, text string, failure bool) {
	if k := a.settingsKit(); k != nil {
		k.Note(c, text, failure)
		return
	}
	ink := c.Theme().TextMuted
	if failure {
		ink = c.Theme().Danger
	}
	ui.Text(c, text).FontSize(12).TextColor(ink).Wrap()
}

// settingsTextWidth 是设置页内容那一栏现在能放字的宽：减去分类栏和左右内距，再夹到页面最宽。
// 行里的字按这个宽截，窄窗口（900）下不折行、不撑出窗口。
func (a *app) settingsTextWidth(c *ui.Context, k *winshell.Kit) float32 {
	w, _ := c.Size()
	var avail float32
	if k != nil {
		avail = w - 2*k.G.PaneInset
		if k.Mode(w) != winshell.Narrow {
			avail = w - k.G.NavWidth - winshell.SettingsListWidth - 2*k.G.PaneInset
		}
		avail = min(avail, winshell.SettingsWidth) - k.G.SpaceSmall
	} else {
		// 旧界面：左边 240 的分类栏，内容栏左右各留 8 个间距，最宽 860。
		avail = min(w-240-2*c.Theme().Space(8), 860)
	}
	return max(avail, 1)
}

// embedMissingLine 把“找不到模型文件：<完整路径>”截成一行：放不下时留头留尾，中间用省略号。
func (a *app) embedMissingLine(c *ui.Context, path string, maxW, size float32) string {
	st := &a.embedUI
	if st.lineFor == path && st.lineW == maxW && st.lineSize == size {
		return st.line
	}
	st.lineFor, st.lineW, st.lineSize = path, maxW, size
	st.line = fitOneLine(c, embedMissingPrefix, path, maxW, size)
	return st.line
}

// fitOneLine 量着截：前缀保留，路径留头留尾。maxW 小于前缀时只留前缀。
func fitOneLine(c *ui.Context, prefix, path string, maxW, size float32) string {
	font := c.Theme().Font
	measure := func(s string) float32 {
		w, _ := c.MeasureText(1e6, ui.Span{Text: s, Font: font, Size: size})
		return w
	}
	if measure(prefix+path) <= maxW {
		return prefix + path
	}
	limit := maxW - measure(prefix)
	if limit <= 0 {
		return prefix
	}
	r := []rune(path)
	best, lo, hi := "…", 0, len(r)
	for lo <= hi {
		n := (lo + hi) / 2
		head := (n + 1) / 2
		tail := n - head
		cand := string(r[:head]) + "…"
		if tail > 0 {
			cand += string(r[len(r)-tail:])
		}
		if measure(cand) <= limit {
			best, lo = cand, n+1
		} else {
			hi = n - 1
		}
	}
	return prefix + best
}

// loadEmbedMissing 读一次缺模型状态和版本号；回来时换了数据源就丢掉。
func (a *app) loadEmbedMissing(reg api.SettingsRegistry) {
	ctx, generation := a.base(), a.srcGen
	go func() {
		v, err := reg.ReadSettingsRegistry(ctx)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.embedUI
			st.busy = false
			if err != nil {
				st.loadErr = err.Error()
				return
			}
			st.loadErr = ""
			st.version = v.Version
			st.missing = v.EmbedMissingModel
		})
	}()
}

// pickEmbedModelsRoot 弹选文件夹；取消什么都不做，选完写绝对路径到 embed.modelsRoot。
func (a *app) pickEmbedModelsRoot(m *api.EmbedMissingModel) {
	st := &a.embedUI
	if st.busy {
		return
	}
	// 不用缺的那个文件所在目录当起点：那通常是模型自己的目录，不是要选的模型根，照着选会写错一层
	start := ""
	gen, parent := a.srcGen, a.win
	go func() {
		dir, err := pickEmbedModelsFolder(parent, "选择嵌入模型根目录", start)
		if err != nil {
			a.post(func() {
				if a.srcGen != gen {
					return
				}
				st := &a.embedUI
				st.done = ""
				st.err = "没有打开选择窗口：" + err.Error()
			})
			return
		}
		if dir == "" { // 取消
			return
		}
		abs, absErr := filepath.Abs(dir)
		a.post(func() {
			if a.srcGen != gen {
				return
			}
			if absErr != nil {
				st := &a.embedUI
				st.done = ""
				st.err = "没有改成：" + absErr.Error()
				return
			}
			a.writeEmbedModelsRoot(abs)
		})
	}()
}

// writeEmbedModelsRoot 写 embed.modelsRoot；成功才提示重新开始接收，失败写明原因并让下一帧重读。
func (a *app) writeEmbedModelsRoot(dir string) {
	reg, ok := a.reader.(api.SettingsRegistry)
	if !ok {
		return
	}
	st := &a.embedUI
	if st.busy {
		return
	}
	raw, err := json.Marshal(dir)
	if err != nil {
		st.done = ""
		st.err = "没有改成：" + err.Error()
		return
	}
	st.busy, st.done, st.err = true, "", ""
	ctx, generation, version := a.base(), a.srcGen, st.version
	go func() {
		v, err := reg.WriteSetting(ctx, api.SettingChange{Key: embedModelsRootKey, Value: raw}, version)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.embedUI
			st.busy = false
			if err != nil {
				var saved *api.SavedSettingsError
				if errors.As(err, &saved) { // 已落盘、只是读回快照失败：不能说没存上
					st.done = embedRestartNote
					st.err = err.Error()
					st.asked = false
					return
				}
				st.err = "没有改成：" + err.Error() // 值不变，不回成功的话
				st.asked = false               // 版本可能旧了，下一帧重读
				return
			}
			st.version = v.Version
			st.missing = v.EmbedMissingModel
			st.done = embedRestartNote
		})
	}()
}
