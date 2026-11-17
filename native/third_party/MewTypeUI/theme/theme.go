// Package theme compiles editable chat themes into immutable, typed tokens.
package theme

import (
	"math"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const Version = 9
const LightBase = "desktop-light"
const DarkBase = "desktop-dark"

// Definition is an editable snapshot. Map membership and pointers preserve explicit zeroes.
// An empty Base denotes a complete, standalone definition; it requires every token.
type Definition struct {
	Version    int                          `json:"version"`
	Base       string                       `json:"base,omitempty"`
	Dark       *bool                        `json:"dark,omitempty"`
	Font       *string                      `json:"font,omitempty"`
	Colors     map[string]string            `json:"colors,omitempty"`
	Metrics    map[string]float32           `json:"metrics,omitempty"`
	Typography map[string]TextDefinition    `json:"typography,omitempty"`
	Motion     map[string]MotionDefinition  `json:"motion,omitempty"`
	Materials  map[string]SurfaceDefinition `json:"materials,omitempty"`
}
type TextDefinition struct {
	Size   *float32 `json:"size,omitempty"`
	Weight *int     `json:"weight,omitempty"`
}
type MotionDefinition struct {
	Duration *float64 `json:"duration,omitempty"` // milliseconds
	Easing   *Easing  `json:"easing,omitempty"`
}
type SurfaceDefinition struct {
	Kind         *WindowMaterial `json:"kind,omitempty"`
	Background   *string         `json:"background,omitempty"`
	Alpha        *float32        `json:"alpha,omitempty"`
	BackdropBlur *float32        `json:"backdropBlur,omitempty"`
	Glass        *GlassMode      `json:"glass,omitempty"`
	Clarity      *float32        `json:"clarity,omitempty"`
}

// Theme exposes no mutable maps or token storage. Definitions returned by Definition are detached.
type Theme struct {
	dark       bool
	colors     Colors
	metrics    Metrics
	typography Typography
	motion     Motion
	materials  Materials
	definition Definition
}

func (t *Theme) Dark() bool              { return t.dark }
func (t *Theme) Colors() *Colors         { return &t.colors }
func (t *Theme) Metrics() *Metrics       { return &t.metrics }
func (t *Theme) Typography() *Typography { return &t.typography }
func (t *Theme) Motion() *Motion         { return &t.motion }
func (t *Theme) Materials() *Materials   { return &t.materials }

// TextStyle sizes are unscaled DIP. Multiply Size by c.Preferences().TextScale exactly once.
type TextStyle struct {
	size   float32
	weight int
}

func (t TextStyle) Size() float32 { return t.size }
func (t TextStyle) Weight() int   { return t.weight }

type Typography struct {
	font   string
	styles [len(textSpecs)]TextStyle
}

func (t *Typography) Font() string                    { return t.font }
func (t *Typography) Body() TextStyle                 { return t.styles[0] }
func (t *Typography) ConversationTitle() TextStyle    { return t.styles[1] }
func (t *Typography) ConversationPreview() TextStyle  { return t.styles[2] }
func (t *Typography) ConversationTime() TextStyle     { return t.styles[3] }
func (t *Typography) HeaderTitle() TextStyle          { return t.styles[4] }
func (t *Typography) HeaderStatus() TextStyle         { return t.styles[5] }
func (t *Typography) Message() TextStyle              { return t.styles[6] }
func (t *Typography) MessageTime() TextStyle          { return t.styles[7] }
func (t *Typography) Quote() TextStyle                { return t.styles[8] }
func (t *Typography) Reaction() TextStyle             { return t.styles[9] }
func (t *Typography) Compose() TextStyle              { return t.styles[10] }
func (t *Typography) Sidebar() TextStyle              { return t.styles[11] }
func (t *Typography) Menu() TextStyle                 { return t.styles[12] }
func (t *Typography) Badge() TextStyle                { return t.styles[13] }
func (t *Typography) ConversationTag() TextStyle      { return t.styles[14] }
func (t *Typography) SidebarBadge() TextStyle         { return t.styles[15] }
func (t *Typography) Avatar() TextStyle               { return t.styles[16] }
func (t *Typography) MessageName() TextStyle          { return t.styles[17] }
func (t *Typography) Service() TextStyle              { return t.styles[18] }
func (t *Typography) LatestBadge() TextStyle          { return t.styles[19] }
func (t *Typography) ContentTitle() TextStyle         { return t.styles[20] }
func (t *Typography) ContentStrong() TextStyle        { return t.styles[21] }
func (t *Typography) ContentCode() TextStyle          { return t.styles[22] }
func (t *Typography) DialogTitle() TextStyle          { return t.styles[23] }
func (t *Typography) DialogBody() TextStyle           { return t.styles[24] }
func (t *Typography) DialogButton() TextStyle         { return t.styles[25] }
func (t *Typography) ComposeTitle() TextStyle         { return t.styles[26] }
func (t *Typography) PickerTitle() TextStyle          { return t.styles[27] }
func (t *Typography) ConversationTTL() TextStyle      { return t.styles[28] }
func (t *Typography) SettingsSectionTitle() TextStyle { return t.styles[29] }
func (t *Typography) InputPlaceholder() TextStyle     { return t.styles[30] }
func (t *Typography) MessageAvatar() TextStyle        { return t.styles[31] }
func (t *Typography) PageTitle() TextStyle            { return t.styles[32] }
func (t *Typography) StatusBadge() TextStyle          { return t.styles[33] }

type Easing string

const (
	Linear       Easing = "linear"
	EaseIn       Easing = "ease-in"
	EaseOut      Easing = "ease-out"
	EaseInOut    Easing = "ease-in-out"
	EaseOutCirc  Easing = "ease-out-circ"
	EaseOutQuint Easing = "ease-out-quint"
	SineInOut    Easing = "sine-in-out"
)

func (e Easing) Curve() ui.Easing {
	switch e {
	case Linear:
		return ui.Linear
	case EaseIn:
		return ui.EaseIn
	case EaseOut:
		return ui.EaseOut
	case EaseOutCirc:
		return func(t float32) float32 { u := t - 1; return float32(math.Sqrt(float64(1 - u*u))) }
	case EaseInOut:
		return ui.EaseInOut
	case EaseOutQuint:
		return func(t float32) float32 { u := 1 - t; return 1 - u*u*u*u*u }
	case SineInOut:
		return func(t float32) float32 { return float32((1 - math.Cos(math.Pi*float64(t))) / 2) }
	}
	panic("uncompiled easing")
}

type Transition struct {
	duration time.Duration
	easing   Easing
}

func (t Transition) Duration() time.Duration { return t.duration }
func (t Transition) Easing() Easing          { return t.easing }

type Motion struct{ transitions [len(motionSpecs)]Transition }

func (m *Motion) Control() Transition               { return m.transitions[0] }
func (m *Motion) Page() Transition                  { return m.transitions[1] }
func (m *Motion) Slide() Transition                 { return m.transitions[2] }
func (m *Motion) Fade() Transition                  { return m.transitions[3] }
func (m *Motion) DateFade() Transition              { return m.transitions[4] }
func (m *Motion) MessageReveal() Transition         { return m.transitions[5] }
func (m *Motion) Menu() Transition                  { return m.transitions[6] }
func (m *Motion) MenuShow() Transition              { return m.transitions[7] }
func (m *Motion) RippleShow() Transition            { return m.transitions[8] }
func (m *Motion) RippleHide() Transition            { return m.transitions[9] }
func (m *Motion) ToLatest() Transition              { return m.transitions[10] }
func (m *Motion) Scrollbar() Transition             { return m.transitions[11] }
func (m *Motion) ListCollapse() Transition          { return m.transitions[12] }
func (m *Motion) ThirdPanel() Transition            { return m.transitions[13] }
func (m *Motion) Online() Transition                { return m.transitions[14] }
func (m *Motion) Typing() Transition                { return m.transitions[15] }
func (m *Motion) TypingHalfPeriod() Transition      { return m.transitions[16] }
func (m *Motion) TypingStagger() Transition         { return m.transitions[17] }
func (m *Motion) MessageRevealList() Transition     { return m.transitions[18] }
func (m *Motion) DateHold() Transition              { return m.transitions[19] }
func (m *Motion) ChatScroll() Transition            { return m.transitions[20] }
func (m *Motion) DateCrossingHold() Transition      { return m.transitions[21] }
func (m *Motion) FileHover() Transition             { return m.transitions[22] }
func (m *Motion) MediaTransferRotate() Transition   { return m.transitions[23] }
func (m *Motion) Dialog() Transition                { return m.transitions[24] }
func (m *Motion) DialogScrim() Transition           { return m.transitions[25] }
func (m *Motion) TooltipDelay() Transition          { return m.transitions[26] }
func (m *Motion) ToastHold() Transition             { return m.transitions[27] }
func (m *Motion) ToastIn() Transition               { return m.transitions[28] }
func (m *Motion) ToastOut() Transition              { return m.transitions[29] }
func (m *Motion) ToastSlide() Transition            { return m.transitions[30] }
func (m *Motion) ViewerWait() Transition            { return m.transitions[31] }
func (m *Motion) ViewerShow() Transition            { return m.transitions[32] }
func (m *Motion) ViewerHide() Transition            { return m.transitions[33] }
func (m *Motion) ViewerHover() Transition           { return m.transitions[34] }
func (m *Motion) SubmenuAim() Transition            { return m.transitions[35] }
func (m *Motion) ViewerLoading() Transition         { return m.transitions[36] }
func (m *Motion) ViewerLoadingRotation() Transition { return m.transitions[37] }
func (m *Motion) ComposeButton() Transition         { return m.transitions[38] }
func (m *Motion) ComposePlaceholder() Transition    { return m.transitions[39] }
func (m *Motion) Picker() Transition                { return m.transitions[40] }
func (m *Motion) PickerSlide() Transition           { return m.transitions[41] }
func (m *Motion) SendingFlight() Transition         { return m.transitions[42] }
func (m *Motion) SendingSlide() Transition          { return m.transitions[43] }
func (m *Motion) SendingMix() Transition            { return m.transitions[44] }
func (m *Motion) SendingTimeout() Transition        { return m.transitions[45] }
func (m *Motion) MediaTransferCatchup() Transition  { return m.transitions[46] }
func (m *Motion) SpoilerFadeIn() Transition         { return m.transitions[47] }
func (m *Motion) SpoilerHold() Transition           { return m.transitions[48] }
func (m *Motion) SpoilerFadeOut() Transition        { return m.transitions[49] }
func (m *Motion) SpoilerFrame() Transition          { return m.transitions[50] }
func (m *Motion) SpoilerImageFadeIn() Transition    { return m.transitions[51] }
func (m *Motion) SpoilerImageFadeOut() Transition   { return m.transitions[52] }
func (m *Motion) RecordActivity() Transition        { return m.transitions[53] }
func (m *Motion) UploadActivity() Transition        { return m.transitions[54] }
func (m *Motion) StickerActivity() Transition       { return m.transitions[55] }
func (m *Motion) Speaking() Transition              { return m.transitions[56] }
func (m *Motion) SpeakingFade() Transition          { return m.transitions[57] }
func (m *Motion) PickerLeave() Transition           { return m.transitions[58] }
func (m *Motion) SettingsToggle() Transition        { return m.transitions[59] }
func (m *Motion) Choice() Transition                { return m.transitions[60] }
func (m *Motion) ChooseSearch() Transition          { return m.transitions[61] }
func (m *Motion) ChooseClear() Transition           { return m.transitions[62] }
func (m *Motion) InputField() Transition            { return m.transitions[63] }
func (m *Motion) SliderHover() Transition           { return m.transitions[64] }
func (m *Motion) ButtonNumbers() Transition         { return m.transitions[65] }
func (m *Motion) Segments() Transition              { return m.transitions[66] }
func (m *Motion) Tabs() Transition                  { return m.transitions[67] }
func (m *Motion) ProfileTabs() Transition           { return m.transitions[68] }
func (m *Motion) ProfileTabsScroll() Transition     { return m.transitions[69] }
func (m *Motion) Collapse() Transition              { return m.transitions[70] }
func (m *Motion) PageTitle() Transition             { return m.transitions[71] }
func (m *Motion) ProfileTabsWheelGrace() Transition { return m.transitions[72] }
func (m *Motion) ProfileTabsWheelRest() Transition  { return m.transitions[73] }

type WindowMaterial string

const (
	None    WindowMaterial = "none"
	Mica    WindowMaterial = "mica"
	Acrylic WindowMaterial = "acrylic"
)

func (m WindowMaterial) Vibrancy() mygo.Vibrancy {
	switch m {
	case None:
		return mygo.VibrancyNone
	case Mica:
		return mygo.VibrancyMica
	case Acrylic:
		return mygo.VibrancyAcrylic
	}
	panic("uncompiled window material")
}

type GlassMode string

const (
	GlassNone    GlassMode = "none"
	GlassRegular GlassMode = "regular"
	GlassClear   GlassMode = "clear"
)

// Surface separates content backdrop blur from the host-owned window material.
type Surface struct {
	kind                         WindowMaterial
	background                   ui.Color
	alpha, backdropBlur, clarity float32
	glass                        GlassMode
}

func (s Surface) Kind() WindowMaterial       { return s.kind }
func (s Surface) Background() ui.Color       { return s.background.Alpha(s.alpha) }
func (s Surface) OpaqueBackground() ui.Color { c := s.background; c.A = 255; return c }
func (s Surface) Alpha() float32             { return s.alpha }
func (s Surface) BackdropBlur() float32      { return s.backdropBlur }
func (s Surface) Glass() GlassMode           { return s.glass }
func (s Surface) Clarity() float32           { return s.clarity }

type Materials struct{ surfaces [12]Surface }

func (m *Materials) Window() Surface       { return m.surfaces[0] }
func (m *Materials) Control() Surface      { return m.surfaces[1] }
func (m *Materials) Conversation() Surface { return m.surfaces[2] }
func (m *Materials) Header() Surface       { return m.surfaces[3] }
func (m *Materials) MessageIn() Surface    { return m.surfaces[4] }
func (m *Materials) MessageOut() Surface   { return m.surfaces[5] }
func (m *Materials) Compose() Surface      { return m.surfaces[6] }
func (m *Materials) Sidebar() Surface      { return m.surfaces[7] }
func (m *Materials) Menu() Surface         { return m.surfaces[8] }
func (m *Materials) Tooltip() Surface      { return m.surfaces[9] }
func (m *Materials) Service() Surface      { return m.surfaces[10] }
func (m *Materials) Overlay() Surface      { return m.surfaces[11] }
