# Theme JSON v9

`theme.Definition` is editable data. Use `theme.Compile` when edits change, then call `theme.Apply(c, compiled)` at the start of each view. `theme.T(c)` returns that source theme; before Apply it returns a shared light/dark preset matching mygo. Token fields are private; access through typed getters such as `t.Colors().Message().Out().Background()` and `t.Metrics().Message().RadiusLarge()`.

```go
d := theme.Definition{
    Version: theme.Version,
    Base: theme.LightBase,
    Colors: map[string]string{
        "window.background": "#ffffff",
        "conversation.background": "@window.background",
        "message.out.background": "#effdde",
    },
    Metrics: map[string]float32{"message.radiusLarge": 0},
    Materials: map[string]theme.SurfaceDefinition{
        "header": {Alpha: new(float32(0.9)), BackdropBlur: new(float32(12))},
    },
}
t, err := theme.Compile(d) // report err to the editor; retain the previous t on failure
```

The example's 12 DIP blur and 0.9 alpha are user overrides, not preset values. The complete color and metric key lists are in reference/theme-colors.md and reference/theme-metrics.md. Typography, motion and material roles are in reference/theme-behavior.md.

JSON objects have these fields:

- `version`: required integer, currently 9. This contract adds 74 glyph bounds, pen and host-artwork metrics to the controls/chat identity contract. Complete snapshots require all 614 colors, 806 metrics, 34 text styles and 74 motion roles. Versions 1–8, including both incompatible v7 contracts, are rejected by `Compile`; `Import` uses the same validation. Sparse definitions should use `theme.Version` and an explicit preset base. No implicit snapshot migration or compatibility aliases are provided.
- `base`: `desktop-light` or `desktop-dark` for sparse overrides. Omit it for a complete standalone snapshot.
- `dark`: optional boolean mode override. Standalone snapshots require it.
- `font`: optional system font family override; empty string means system font. Standalone snapshots require it.
- `colors`: dotted role keys to `#RRGGBB`, `#RRGGBBAA` or `@other.role`. Hex alpha is last. Literal alpha zero preserves RGB. References resolve across the completed color table and may form chains; missing and cyclic references are errors.
- `metrics`: dotted role keys to nonnegative finite numbers. Geometry uses DIP; opacity, counts, ratios and zoom levels are unitless; `viewer.loadingMinArc` uses degrees. Zero is an explicit override.
- `typography`: role keys to objects with `size` (positive finite DIP) and `weight` (1..1000). Both may be individually omitted when inheriting a base.
- `motion`: role keys to objects with `duration` (nonnegative finite milliseconds, including fractional milliseconds within time.Duration range) and `easing` (`linear`, `ease-in`, `ease-out`, `ease-in-out`, `ease-out-circ`, `ease-out-quint`, `sine-in-out`). Circular/quintic/sine curves follow the Telegram source equations listed in reference/chat.md.
- `materials`: surface keys to objects with `kind`, `background`, `alpha`, `backdropBlur`, `glass`, `clarity`. `kind` accepts `none`, `mica`, `acrylic`, with system materials only on `window`. Background uses the same literal/reference syntax as colors. Alpha and clarity are in 0..1; backdropBlur is nonnegative finite DIP; glass is `none`, `regular`, or `clear`.

Unknown keys and explicit `null` are errors. Error messages include the dotted key, including nested field/type errors. The parser uses only the standard library. File reading, choosing a file and persisting editor state belong to the host.

`theme.Import(data)` validates and returns an editable definition. `theme.Export(d)` validates and returns indented JSON with every inherited token, no base dependency, and the author's aliases retained. `t.Definition()` returns a detached complete definition. A standalone definition requires all known roles and every nested property. Expanding inherited preset values protects a saved theme from changes to built-in presets; changing the format's token contract requires a version change.

`Compile` reuses the most recently compiled result when definition contents are unchanged, including mutable map and pointer contents. It keeps one detached cache entry, so editor history cannot grow a process-wide cache. `Light()` and `Dark()` each return a shared preset. `Apply` caches the ui.Theme mapping per window and rebuilds it only when the source pointer or system text scale changes. Source tokens remain at 100%; ui.Theme.FontSize receives system TextScale once. Components also multiply raw typography sizes once when measuring and drawing, and should key text measurement by typography/geometry values rather than Theme pointer identity.

The ui.Theme mapping uses window background/text/mutedText; control surface states, border, primary states and focus; semantic danger/warning/success; selection background; toast inverse surface/text; scrollbar thumb and width; control radiusLarge/spacing; body font size and family. Message and quote roles remain separate from Accent.

Window material remains host-owned: use `t.Materials().Window().Kind().Vibrancy()` when creating or updating the window. Components can check `c.Vibrancy()` and use a surface's `OpaqueBackground()` when system material is unavailable. `Background()` multiplies the palette alpha by the surface alpha, affecting the background only. `BackdropBlur()` configures `glass.Blur{Radius: ...}` on a surface; it never changes the system blur radius or blurs text. Optional glass mode and clarity configure `glass.Glass`. No native material support is implied by the headless theme tests.

Regenerate metric accessors and their source table from the recorded clean-room registry with `go run ./internal/cmd/themegen -metrics-only`. `go run ./internal/cmd/themegen -recorded` also rebuilds color accessors from the checked-in palette records, including the Profile Tabs outline roles. Both modes read no upstream sources. The full `go run ./internal/cmd/themegen` regenerates colors from upstream; `-sources` selects the upstream checkout parent and `-out` selects the repository output root.

Glyph width/height metrics describe Telegram target bounds. Replacement artwork uses one scale and may leave space on one axis; the original-ink aspect and output-pen constraints take priority over filling those targets. Layout canvases and hit areas remain independent. This correction adds or removes no theme fields or defaults, so snapshots remain v9.
