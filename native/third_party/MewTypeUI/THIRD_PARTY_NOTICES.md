# Third-party notices

The component implementation is original Go code. Telegram source files were consulted for style values and observable layout behavior; no Telegram code, artwork, images, icons or fonts are distributed.

## Lucide icons 1.58.0

`dialogs/icons.go` uses SVG path data from the Lucide assets in CrychicUI/icon/svg, originally lucide-static 1.58.0. Source: https://lucide.dev / https://github.com/lucide-icons/lucide. Common SVG attributes are combined into one constructor; path geometry is unchanged.

The dialogs subscription badge also uses Lucide `star.svg` from that collection.

`chat/icons.go` embeds arrow-left, chevron-down, check, check-check, clock, eye, message-circle, pin, volume-x, search, ellipsis-vertical, x and phone from the same sources. SVG wrappers were compacted; path geometry is unchanged. No CrychicUI component implementation was copied.

`content/icons.go` contains SVG paths for file, play, pause, download, x and copy from https://lucide.dev, as distributed in CrychicUI's `icon/svg` directory (lucide-static 1.58.0). Paths retain their native 24×24 viewBox. Component implementations are original; CrychicUI's constructor/API conventions were studied without copying its Go implementation.

`overlay/icons.go` embeds the SVG geometry of x, chevron-left, chevron-right, check, zoom-in and zoom-out from Lucide, obtained from the local CrychicUI `icon/svg` collection. Attributes are shared in a small SVG constructor; geometry is unchanged. Source: https://lucide.dev and https://github.com/lucide-icons/lucide. No Telegram artwork is included.

`compose/action.go` embeds the paperclip, smile, send, mic, check, x and file SVG paths from https://lucide.dev (npm lucide-static 1.58.0), as distributed in CrychicUI's `icon/svg`. SVG wrapper attributes are consolidated; no Telegram assets are included.

Compose reference actions also embed Lucide reply and pencil from the same collection.

`settings/settings.go` embeds Lucide chevron-left, chevron-right, check and search geometry; `gallery/settings.go` embeds lock from the same local Lucide 1.58.0 collection (ISC/MIT). Geometry is unchanged, SVG wrapper attributes are consolidated. Settings implementation studied CrychicUI `form/settings_form.go`, `choice/search_dropdown.go` and `input/number_field.go` for host ownership, focus and API patterns, without copying their implementations. The clean-room implementation read only the repository's Telegram paint specifications and source-value records.

`control/control.go` reuses Lucide arrow-left, x and chevron-down geometry from the existing ISC/MIT collection for navigation and dropdown affordances. Controls studied CrychicUI `button/icon_button.go` and `navigation/vertical_tabs.go` for ButtonBase, accessible names and host-owned selection, plus mygo's editor/Handle/Effect APIs. No CrychicUI component implementation or Telegram source/assets were copied; implementation used the repository's clean-room paint specifications.

`gallery/icon_glyph.go` embeds Lucide `house.svg` from the same CrychicUI/icon/svg collection (1.58.0, ISC/MIT). `internal/draw/glyph.go` wraps the licensed paths with independent scale/translation and pen-width adjustments to match measured glyph bounds; the original path data is retained. No Telegram bitmap or path is embedded.

ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

## Feather-derived Lucide icons

The redistributed Lucide subset includes Feather-derived artwork. Source: https://github.com/feathericons/feather.

The MIT License (MIT)

Copyright (c) 2013-present Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

## Linked dependencies

The Go dependencies are linked modules, with their own license files. mygo v0.7.4 is MIT. The component package does not depend on CrychicUI.

## Implementation references

CrychicUI's component signatures, gallery registry and mygo Handle/Tester conventions were studied. Overlay behavior and layout implementation are independently written; no CrychicUI implementation code was copied. Telegram source was consulted for numerical declarations and externally visible behavior; no C++ implementation or assets were copied.

Native overlay ownership and flyout API usage were studied in mygo v0.7.4 (`flyout.go`, `flyout_test.go`, `examples/flyout/main.go`, MIT). Independent Go code uses those public APIs; no additional third-party component implementation or assets were copied.

## CrychicUI patterns

The compose input/keyboard and gallery API patterns were studied in
https://github.com/ELY-Open-Source/CrychicUI (`chat/composer.go`,
`gallery/registry.go`). The components are independently implemented, with host-owned
submission and no dependency on CrychicUI. The icons above are the copied material.

Message-bottom implementation also studied CrychicUI `chat/reactions.go` and `chat/markdown_message.go`, and mygo v0.7.4 `ui/richtext.go`, `textobject_test.go`, `text_layout.go` and `layout.go` (MIT): ButtonBase identity, host-owned content, retained Markdown state, shaped inline objects and flex wrapping. The implementation is original Go written against `docs/reference/paint/message-bottom.md`; no Telegram/lib_ui source was opened during this implementation task.

## Twemoji gallery artwork v17.0.3

The four unmodified SVGs in `gallery/emoji/` (1f600, 1f44b, 1f33f, 2b50)
are Twemoji graphics by Twitter, Inc. and other contributors, from the community
release https://github.com/jdecked/twemoji/tree/v17.0.3/assets/svg .
Graphics are licensed under Creative Commons Attribution 4.0 International:
https://creativecommons.org/licenses/by/4.0/ . No Twemoji JavaScript is included.
The gallery embeds this small host-prepared sample; components accept host images
or Unicode text and do not fetch artwork. The footer clock uses Lucide geometry
under the notices above. The filled folder in dialogs/icons.go is original
artwork by the MewTypeUI contributors, under this repository's MIT license.
