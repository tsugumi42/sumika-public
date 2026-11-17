package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	xdraw "golang.org/x/image/draw"
)

// captureMenu exercises the public native flyout path. Only windows created by
// this process are enumerated; reference images are never opened in this mode.
func captureMenu(path string) error {
	var host *mygo.Window
	state := overlay.MenuState{}
	items := make([]overlay.MenuItem, 8)
	for i := range items {
		items[i] = overlay.MenuItem{ID: fmt.Sprint(i), Label: fmt.Sprintf("菜单区域 %02d", i+1)}
	}
	result := make(chan error, 1)
	mygo.App.WhenReady(func() {
		host = mygo.NewWindow(mygo.WindowOptions{Title: "Fidelity menu capture", Width: 400, Height: 500, UseContentSize: true, Hidden: true, Content: ui.View(func(c *ui.Context) {
			theme.Apply(c, theme.Light())
			overlay.Native(c, host, func() {
				target := ui.Button(c, "菜单锚点")
				overlay.ContextMenu(c, target, items, &state, overlay.MenuOptions{Icons: true, Label: "Fidelity menu"})
			})
		})})
		host.OnReadyToShow(func() { host.Show(); host.Update(func() { state.ShowAt(ui.Point{X: 40, Y: 40}) }) })
		go func() {
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			var seen time.Time
			for {
				select {
				case <-deadline.C:
					result <- fmt.Errorf("native menu did not become ready")
					mygo.App.Quit()
					return
				case <-ticker.C:
					windows := mygo.Windows()
					if len(windows) < 2 {
						continue
					}
					if seen.IsZero() {
						seen = time.Now()
						continue
					}
					if time.Since(seen) < theme.Light().Motion().MenuShow().Duration() {
						continue
					}
					popup := windows[len(windows)-1]
					data, err := popup.CapturePage()
					if err == nil {
						src, e := png.Decode(bytes.NewReader(data))
						err = e
						if err == nil {
							w, h := popup.ContentSize()
							dst := image.NewRGBA(image.Rect(0, 0, int(float64(w)*1.25+.5), int(float64(h)*1.25+.5)))
							xdraw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
							err = savePNG(path, dst)
							if err == nil {
								err = saveJSON(path+".json", map[string]any{"logical_width": w, "logical_height": h, "capture_pixels": []int{src.Bounds().Dx(), src.Bounds().Dy()}, "normalized_scale": 1.25, "rows": 8})
							}
						}
					}
					result <- err
					mygo.App.Quit()
					return
				}
			}
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return err
	}
	return <-result

}
