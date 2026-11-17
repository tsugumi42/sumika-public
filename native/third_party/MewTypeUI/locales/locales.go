// Package locales contains the component library's host-overridable messages.
package locales

import (
	"embed"

	"github.com/egoist/mygo/i18n"
)

//go:embed en.json zh-Hans.json
var messages embed.FS

var catalog = func() *i18n.Catalog {
	c, err := i18n.NewLibraryCatalog(messages, "en", "mewtypeui")
	if err != nil {
		panic(err)
	}
	return c
}()

// Text follows the window locale and application overrides of mewtypeui.<key>.
func Text(locale, key string) string { return catalog.Format(locale, key, nil) }
