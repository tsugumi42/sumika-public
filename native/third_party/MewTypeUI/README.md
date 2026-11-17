# MewTypeUI

A chat-oriented UI component library for [mygo](https://github.com/egoist/mygo), modeled on the look and feel of the Telegram Desktop client: conversation lists, chat headers, message bubbles, replies, reactions, media, compose area and menus, plus a fully editable theme (colors, radii, spacing, type scale, motion, translucency).

The name comes from 夢限大みゅーたいぷ (Mugendai Mewtype), following mygo and CrychicUI.

> Status: early development. APIs will change.

## Principles

- **Built on mygo.** Components take `*ui.Context`, return `ui.Element`, and follow mygo's identity and lifetime rules (`docs/architecture.md`).
- **Telegram Desktop as the visual target.** Sizes, radii, type and motion durations are taken from the Telegram Desktop style sources and recorded in `docs/reference/` with file and line. We reimplement the design in our own code; no Telegram code, assets, names or logos are copied.
- **Everything themeable.** Named color roles (inspired by Telegram's palette), metrics and materials live in one theme that can be edited, imported and exported.
- **Host owns data.** Components never load files, hit the network or touch a database; the host passes prepared state.

## Layout

The `layout` package provides a responsive window shell, keyed pane slots, draggable and keyboard-accessible dividers, third-pane transitions, and native title-control clearance. See [layout behavior and source mapping](docs/reference/layout.md). Run the native gallery with `go run ./cmd/gallery`; use `-page layout/chat -focus` for the complete chat composition.

The complete chat page composes the six component areas: search and keyed conversations, grouped messages with replies/media/reactions, a working editor and picker, context menus, image viewing and third-pane navigation. Its host state preserves message IDs and reply metadata when sending and clearing drafts.

See `docs/architecture.md` for the wider package design and component plan.

## Local fidelity acceptance

Run `go run ./cmd/gallery -page fidelity/14-44-57 -focus -reference <local.png>` to compare the corresponding invented scene. The gallery's **选择参考图** button loads another local PNG/JPEG; the page menu selects any registered page. **半透明** blends the reference, **差异** displays absolute RGB differences, and **并排对照** keeps the original comparison layout. Offsets are page-local DIP; reference scale defaults to 1.25 physical pixels per DIP. Arrow keys on the offset sliders step one reference pixel; zoom and opacity are adjustable.

`go run ./cmd/fidelity -capture-menu <outside-worktree>/menu.png` captures the existing native menu renderer. Then run `go run ./cmd/fidelity -references <screenshot-directory> -menu-render <outside-worktree>/menu.png -out <outside-worktree>/results -report <outside-worktree>/report.md`. It reads only the fourteen explicit filenames, renders actual components at 125%, exports local overlay/difference images and numeric CSV/JSON tables, and ranks twenty region observations. `-before <previous>/observations.json` adds before/after deltas. Reference crops are manually annotated; text-dependent widths, avatar/media pixels and client extensions are excluded from geometry scoring.

For the menu comparison page, run `go run ./cmd/gallery -page fidelity/14-44-28 -focus -reference <local.png> -menu-render <outside-worktree>/menu.png`. This places the actual captured native menu inside the page layer so both overlay modes apply; independent native flyout windows cannot inherit the main window's effect.

The initial measurements and scope are recorded in [fidelity-2026-11-03.md](docs/reference/fidelity-2026-11-03.md); [paint coverage](docs/reference/paint/fidelity-coverage.md) distinguishes missing paint contracts and components from implementation defects. Diagnostic images contain the local reference and must remain outside the repository.

## Theme

The `theme` package includes light and night presets, 614 named color roles (all 586 palette entries plus 28 semantic mappings), 806 geometry/opacity/count tokens, 34 text styles, 74 motion roles and material settings. Editable JSON v9 definitions compile to typed tokens; `Apply` maps the common roles to mygo and caches them per window. Replacement icons keep their layout canvases and use uniform path scaling, independently measured ink bounds and pen widths; see [inventory and sources](docs/reference/icon-glyph.md).

Use `theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase})`, then call `theme.Apply(c, t)` at the start of each view. Read chat-specific tokens through `theme.T(c)`. See [theme format and API](docs/theme-format.md), [color sources](docs/reference/theme-colors.md), [metric sources](docs/reference/theme-metrics.md), and [mapping exceptions](docs/reference/exceptions.md).

## Conversation lists

The `dialogs` package provides conversation rows, a virtual list keyed by host IDs, search and scope controls, folder rails and group headings, including compact rows, stories, call spectra, corner badges, promoted/verification rows, drafts, distinct activity animations, receipts, unread state, tags and forum previews. See [API and host ownership](docs/dialogs.md), [Telegram mapping](docs/reference/dialogs.md) and the registered pages in `gallery/dialogs.go`.

## Chat body

`chat` provides headers and icon actions, incoming/outgoing message rows and bubble shells, grouped corners and tails, inline timestamps/receipts, service/date/unread markers, floating dates, jump-to-latest navigation and explicit new-message reveals. `BubbleOptions.Body` and `Footer` receive the shared bottom-information reservation for custom Markdown and reactions. Bottom photos/albums own the image information overlay and keep reactions outside the shell. Hosts retain stable message IDs, selection, history scrolling and arrival events.

See [chat API and source behavior](docs/reference/chat.md) and [documented differences](docs/reference/exceptions.md). `gallery/chat.go` and `gallery/chat_identity.go` register eleven pages, including a working history composition.

`Message.AvatarInitials` supplies prepared initials for missing/loading/failed images; `AvatarLabel` supplies the full accessible name and `AvatarColor` optionally supplies a solid background. `Row` paints these inside its existing 33 DIP group-end/viewport avatar. `Message.SenderNameColor` optionally overrides only that message's name in both `Bubble` and `Row`; zero uses the original theme role. Run `-page chat/identity -focus` for the resource and color states. See the [clean-room identity paint contract](docs/reference/paint/chat-identity.md).

`chat.RetainedBody` accepts a host-owned `BodyState`, prepared spans, the actual available DIP width/font/direction, and the shared trailing reservation. It reuses shaped geometry for paint, objects, links and selection until those inputs change. Keep one state per window/message/paragraph and discard it when its virtual row leaves the viewport. Attach one `BodySelection` to the bubble content root before building its paragraphs and call `Finish` afterward; `CopyData` receives original paragraph IDs and rune ranges so the host can restore inline image text. Links expose accessible names and URLs, per-fragment tooltip anchors, and a `ClickedLink` action for the host to validate and open.

## Message content

The `content` package provides reply quotes, reaction pills, forward headers, link previews, Markdown, photos, albums, stickers, GIF/video covers, file rows and voice waveforms. Persistent interaction state and prepared bitmaps belong to the host. [Content API](docs/content.md) documents callbacks and sizing; [content sources](docs/reference/content.md) records Telegram geometry and runtime behavior. `gallery/content.go` registers 13 interactive pages.

## Overlays

The `overlay` package supplies native context/popup menus and submenus, nonactivating tooltip windows, a monitor-wide image viewer, modal boxes and toasts. Wrap each native window's view in `overlay.Native(c, window, content)` after `theme.Apply`; the binding owns transient windows and closes them with their owner or component. All visual tokens come from `theme.T(c)`. Interactive pages register in `gallery/overlay.go`; API and state contracts, source locations and platform exceptions are recorded in [the overlay reference](docs/reference/overlay.md).

## Compose

The `compose` package provides a growing chat editor, source reply/edit bars, action buttons, an emoji/sticker/GIF picker with default editor docking, and keyed send-confirmation previews with media albums and file states. See [compose API and state coverage](docs/reference/compose.md). Seven interactive pages register in `gallery/compose.go`, including native IME verification.

Picker items separate their Unicode `Emoji` sequence from the accessible `Label`; hosts may supply prepared Twemoji artwork through `Image`, or use the system text renderer. Built-in compose messages use mygo i18n with English and Simplified Chinese catalogs (`MewTypeUI.<key>` application overrides). Run `go run ./cmd/gallery -page compose/picker -focus -locale zh-Hans` to inspect the localized picker, or `-page dialogs/folders` for clipped folder transitions.

## Settings

The `settings` package provides section titles and dividers, value/icon/arrow rows, toggles, radio/check marks and rows, a searchable grouped choice dialog, precise integer fields and discrete numeric sliders. Hosts retain values, validation and selection; components perform no I/O. [API and host contracts](docs/settings.md) link the six clean-room paint specifications and source tables. Nine gallery pages include a complete model settings example with five providers and 40 invented models, plus a 200-item chooser.

## General controls

The `control` package provides round/link/icon buttons, single/multiline inputs, dropdowns, discrete label segments, underline/profile tabs, side categories, page titles, status/count badges, collapsible content and approval cards. `content.ForwardSummary` renders host-prepared forwarded summaries; `compose.Editor` exposes host-owned generation/stop state; `sound.Dispatch` reports eligible playback requests without owning an audio engine. [API, state ownership and validation](docs/controls.md) describe the boundaries. Fifteen `controls/*` gallery pages cover the registered states.

## License

MIT. Third-party icon sources and licenses are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
