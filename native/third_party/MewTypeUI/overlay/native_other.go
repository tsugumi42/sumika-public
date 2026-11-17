//go:build !windows

package overlay

import "github.com/egoist/mygo"

func placeViewer(window, parent *mygo.Window) {
	window.SetBounds(mygo.Screen.DisplayMatching(parent.Bounds()).Bounds)
	window.SetFullScreen(true)
}
