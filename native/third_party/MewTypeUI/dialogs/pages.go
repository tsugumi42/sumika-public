package dialogs

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type folderVisual struct {
	current, previous string
	backward          bool
	bounds            ui.Handle
}

// FolderPages fills a host slot with the selected folder's content. IDs are in
// visual order; selected must belong to ids. The host retains each folder's data
// and scroll state. During a switch content is called for both visible folders.
func FolderPages(c *ui.Context, ids []string, selected string, content func(string)) ui.Element {
	t := theme.T(c)
	root := ui.Box(c).Fill().MinWidth(0).MinHeight(0).Clip().Background(t.Materials().Conversation().Background())
	v := ui.Local(root, "folders", func() folderVisual { return folderVisual{current: selected, previous: selected} })
	root.Bind(&v.bounds)
	if v.current != selected {
		v.previous, v.current = v.current, selected
		v.backward = slices.Index(ids, selected) < slices.Index(ids, v.previous)
		root.Jump("progress", 0)
	}
	p := animate(root, "progress", 1, t.Motion().Page())
	w := v.bounds.Bounds(c).W
	if w == 0 || v.previous == v.current {
		p = root.Jump("progress", 1)
	}
	boundary, shift := w*(1-p), t.Metrics().Sidebar().SlideShift()*p
	root.Children(func() {
		if p == 1 {
			ui.Box(c.Key(v.current)).Fill().Children(func() { content(v.current) })
			return
		}
		// Partition the viewport at the covering page's edge. Parallax changes
		// the underlying content position, never its visible clip boundary.
		under, over := v.previous, v.current
		if v.backward {
			under, over = v.current, v.previous
			boundary, shift = w*p, t.Metrics().Sidebar().SlideShift()*(1-p)
		}
		ui.Box(c.Key("under")).Absolute().Left(0).Top(0).Width(boundary).FillHeight().Clip().IgnoreInput(true).Children(func() {
			ui.Box(c.Key(under)).Absolute().Left(-shift).Top(0).Width(w).FillHeight().Children(func() { content(under) })
			alpha := p
			if v.backward {
				alpha = 1 - p
			}
			ui.Box(c).Absolute().Fill().Background(t.Colors().Control().SlideFadeOutBackground().Alpha(alpha))
		})
		ui.Box(c.Key("over")).Absolute().Left(boundary).Top(0).Right(0).FillHeight().Clip().IgnoreInput(true).Children(func() {
			ui.Box(c.Key(over)).Width(w).FillHeight().Children(func() { content(over) })
		})
	})
	return root
}
