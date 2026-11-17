// Command gallery runs the native component gallery.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/locales/zh-Hans"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/gallery"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func main() {
	m := theme.Light().Metrics().Layout()
	page := flag.String("page", "layout/chat", "registered page ID")
	dark := flag.Bool("dark", false, "start with the night theme")
	focus := flag.Bool("focus", false, "hide gallery navigation for a full-width preview")
	locale := flag.String("locale", "", "interface language: en or zh-Hans; empty follows system")
	width := flag.Int("width", int(m.BigWidth()), "initial window width in DIP")
	height := flag.Int("height", int(m.BigHeight()), "initial window height in DIP")
	reference := flag.String("reference", "", "local PNG/JPEG reference; never copied into the repository")
	menuRender := flag.String("menu-render", "", "local native menu capture for the fidelity menu page")
	comparison := flag.String("comparison", "alpha", "alpha, difference, or side-by-side")
	referenceScale := flag.Float64("reference-scale", 1.25, "reference physical pixels per DIP")
	referenceX := flag.Float64("reference-x", 0, "reference horizontal offset in page-local DIP")
	referenceY := flag.Float64("reference-y", 0, "reference vertical offset in page-local DIP")
	referenceZoom := flag.Float64("reference-zoom", 1, "reference zoom multiplier")
	flag.Parse()
	mygo.App.SetLanguages("en", "zh-Hans")
	if *locale != "" {
		mygo.App.SetLocale(*locale)
	}
	a := gallery.NewApp()
	a.Selected, a.Dark, a.Focus = *page, *dark, *focus
	found := false
	for _, p := range gallery.Pages() {
		if p.ID == *page {
			found = true
		}
	}
	if !found {
		log.Fatalf("unknown page ID %q", *page)
	}
	loadBitmap := func(path string) *ui.Bitmap {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		bitmap, err := ui.DecodeBitmap(data)
		if err != nil {
			log.Fatal(err)
		}
		return bitmap
	}
	if *reference != "" {
		a.SetReference(loadBitmap(*reference))
	}
	if *menuRender != "" {
		a.SetFidelityMenu(loadBitmap(*menuRender))
	}
	if (*comparison != "alpha" && *comparison != "difference" && *comparison != "side-by-side") || *referenceScale <= 0 || *referenceZoom <= 0 {
		log.Fatal("comparison must be alpha, difference, or side-by-side; reference scale and zoom must be positive")
	}
	a.Comparison.Mode, a.Comparison.Scale, a.Comparison.Zoom = *comparison, *referenceScale, *referenceZoom
	a.Comparison.X, a.Comparison.Y = *referenceX, *referenceY
	a.Compare = *comparison == "side-by-side"
	a.OnReference = func() {
		go func() {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.Window, Title: "选择本地参考图", Filters: []mygo.FileFilter{{Name: "Images", Extensions: []string{"png", "jpg", "jpeg"}}}})
			if err != nil {
				log.Print(err)
				return
			}
			if len(paths) == 0 {
				return
			}
			data, err := os.ReadFile(paths[0])
			if err != nil {
				log.Print(err)
				return
			}
			bitmap, err := ui.DecodeBitmap(data)
			if err != nil {
				log.Print(err)
				return
			}
			a.Window.Update(func() { a.SetReference(bitmap) })
		}()
	}
	a.OnAppearance = func(dark bool) {
		source := mygo.ThemeLight
		if dark {
			source = mygo.ThemeDark
		}
		mygo.Theme.SetSource(source)
	}
	a.OnAppearance(a.Dark)
	mygo.App.WhenReady(func() {
		w := mygo.NewWindow(mygo.WindowOptions{
			Title: "MewTypeUI gallery", Width: *width, Height: *height,
			MinWidth: int(m.MinWidth()), MinHeight: int(m.MinHeight()),
			UseContentSize: true,
			Hidden:         true,
			TitleBarStyle:  mygo.TitleBarHidden, Content: ui.View(a.View),
		})
		a.Window = w
		// The source limits describe the content; mygo's minimum includes the native frame.
		outer := w.Bounds()
		contentWidth, contentHeight := w.ContentSize()
		w.SetMinimumSize(int(m.MinWidth())+outer.Width-contentWidth, int(m.MinHeight())+outer.Height-contentHeight)
		workArea := func() mygo.Rectangle {
			r := w.Bounds()
			return mygo.Screen.DisplayNearestPoint(mygo.Point{X: r.X + r.Width/2, Y: r.Y + r.Height/2}).WorkArea
		}
		a.WindowHost = &layout.WindowHost{
			Fixed: func() bool { return w.IsMaximized() || w.IsFullScreen() },
			CanExtendWithoutMove: func(delta float32) bool {
				r := w.Bounds()
				area := workArea()
				return r.X+r.Width+int(delta) <= area.X+area.Width
			},
			ExtendWidth: func(delta float32) float32 {
				r := w.Bounds()
				area := workArea()
				before := r.Width
				r.Width = min(r.Width+int(delta), area.Width)
				r.X = max(area.X, min(r.X, area.X+area.Width-r.Width))
				w.SetBounds(r)
				return float32(w.Bounds().Width - before)
			},
		}
		w.OnReadyToShow(w.Show)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
