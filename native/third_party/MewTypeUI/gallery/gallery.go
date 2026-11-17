// Package gallery holds the component showcase pages. Each component area
// registers its pages from its own file; cmd/gallery lists and renders them.
package gallery

import (
	"sort"

	"github.com/egoist/mygo/ui"
)

// Page is one showcase page: every state of a component laid out side by side.
type Page struct {
	ID    string // stable identity, e.g. "layout/window"
	Area  string // component area, e.g. "dialogs", "chat"
	Title string
	View  func(c *ui.Context)
}

var pages []Page

// Register adds a page. Call it from an init function in the area's page file.
func Register(p Page) { pages = append(pages, p) }

// Pages returns the registered pages sorted by area, then title.
func Pages() []Page {
	out := append([]Page(nil), pages...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Area != out[j].Area {
			return out[i].Area < out[j].Area
		}
		return out[i].Title < out[j].Title
	})
	return out
}
