package theme

import (
	"maps"
	"sync"
)

type colorSpec struct{ key, light, dark string }
type metricSpec struct {
	key   string
	value float32
}
type textSpec struct {
	key    string
	size   float32
	weight int
}

var textSpecs = [...]textSpec{
	{"body", 13, 400}, {"conversationTitle", 13, 600}, {"conversationPreview", 13, 400}, {"conversationTime", 13, 400},
	{"headerTitle", 13, 600}, {"headerStatus", 13, 400}, {"message", 13, 400}, {"messageTime", 13, 400},
	{"quote", 13, 400}, {"reaction", 12, 400}, {"compose", 13, 400}, {"sidebar", 11, 600}, {"menu", 13, 400}, {"badge", 12, 700},
	{"conversationTag", 10, 400}, {"sidebarBadge", 12, 600}, {"avatar", 20, 600},
	{"messageName", 13, 600}, {"service", 13, 600}, {"latestBadge", 13, 600},
	{"contentTitle", 13, 600}, {"contentStrong", 13, 700}, {"contentCode", 13, 400},
	{"dialogTitle", 16, 600}, {"dialogBody", 14, 400}, {"dialogButton", 14, 600},
	{"composeTitle", 13, 600}, {"pickerTitle", 13, 600},
	{"conversationTTL", 9, 600},
	{"settingsSectionTitle", 14, 600}, {"inputPlaceholder", 14, 600},
	// paint/chat-identity.md#圆与首字: 33 DIP chat avatar, independent of dialogs.
	{"messageAvatar", 13, 600},
	{"pageTitle", 14, 600}, {"statusBadge", 10, 600},
}

type motionSpec struct {
	key    string
	ms     float64
	easing Easing
}

var motionSpecs = [...]motionSpec{
	{"control", 120, EaseOut}, {"page", 240, EaseOutCirc}, {"slide", 150, EaseOut}, {"fade", 200, Linear},
	{"dateFade", 200, Linear}, {"messageReveal", 150, EaseOutCirc}, {"menu", 150, Linear}, {"menuShow", 200, EaseOutCirc},
	{"rippleShow", 650, EaseOutQuint}, {"rippleHide", 200, Linear}, {"toLatest", 150, Linear}, {"scrollbar", 150, Linear},
	{"listCollapse", 120, EaseOutCirc}, {"thirdPanel", 240, EaseOutCirc},
	{"online", 150, Linear}, {"typing", 800, EaseOutCirc}, {"typingHalfPeriod", 320, Linear}, {"typingStagger", 150, Linear},
	{"messageRevealList", 150, EaseOutQuint}, {"dateHold", 1000, Linear}, {"chatScroll", 240, SineInOut},
	{"dateCrossingHold", 3000, Linear},
	{"fileHover", 200, Linear}, {"mediaTransferRotate", 3000, Linear},
	{"dialog", 200, Linear}, {"dialogScrim", 200, EaseOutCirc}, {"tooltipDelay", 1000, Linear},
	{"toastHold", 1500, Linear}, {"toastIn", 200, Linear}, {"toastOut", 1000, Linear}, {"toastSlide", 160, Linear},
	{"viewerWait", 1100, Linear}, {"viewerShow", 200, Linear}, {"viewerHide", 600, Linear}, {"viewerHover", 150, Linear},
	{"submenuAim", 300, Linear},
	{"viewerLoading", 350, Linear}, {"viewerLoadingRotation", 3000, Linear},
	{"composeButton", 120, Linear}, {"composePlaceholder", 100, Linear}, {"picker", 200, Linear}, {"pickerSlide", 200, Linear},
	{"sendingFlight", 280, Linear}, {"sendingSlide", 160, EaseOutQuint}, {"sendingMix", 110, Linear}, {"sendingTimeout", 1000, Linear},
	{"mediaTransferCatchup", 350, Linear}, {"spoilerFadeIn", 200, Linear}, {"spoilerHold", 200, Linear}, {"spoilerFadeOut", 200, Linear}, {"spoilerFrame", 33, Linear},
	{"spoilerImageFadeIn", 300, Linear}, {"spoilerImageFadeOut", 300, Linear},
	{"recordActivity", 500, Linear}, {"uploadActivity", 500, Linear}, {"stickerActivity", 2000, Linear},
	{"speaking", 3200, Linear}, {"speakingFade", 400, Linear},
	{"pickerLeave", 300, Linear},
	{"settingsToggle", 120, Linear}, {"choice", 120, Linear},
	{"chooseSearch", 150, Linear}, {"chooseClear", 150, Linear},
	{"inputField", 150, Linear}, {"sliderHover", 150, Linear},
	{"buttonNumbers", 150, EaseOutCirc}, {"segments", 150, Linear}, {"tabs", 150, Linear},
	{"profileTabs", 200, EaseOutQuint}, {"profileTabsScroll", 150, EaseOutCirc},
	{"collapse", 150, Linear}, {"pageTitle", 150, Linear},
	{"profileTabsWheelGrace", 250, Linear}, {"profileTabsWheelRest", 1000, Linear},
}
var surfaceSpecs = [...]struct{ key, background string }{
	{"window", "window.background"}, {"control", "control.surfaceBackground"}, {"conversation", "conversation.background"},
	{"header", "header.background"}, {"messageIn", "message.in.background"}, {"messageOut", "message.out.background"},
	{"compose", "compose.background"}, {"sidebar", "sidebar.background"}, {"menu", "menu.background"}, {"tooltip", "tooltip.background"},
	{"service", "service.background"}, {"overlay", "control.layerBackground"},
}

func preset(base string) Definition {
	dark := base == DarkBase
	d := Definition{Version: Version, Dark: new(dark), Font: new(""), Colors: map[string]string{}, Metrics: map[string]float32{}, Typography: map[string]TextDefinition{}, Motion: map[string]MotionDefinition{}, Materials: map[string]SurfaceDefinition{}}
	for _, s := range colorSpecs {
		v := s.light
		if dark {
			v = s.dark
		}
		d.Colors[s.key] = v
	}
	for _, s := range metricSpecs {
		d.Metrics[s.key] = s.value
	}
	for _, s := range textSpecs {
		d.Typography[s.key] = TextDefinition{new(s.size), new(s.weight)}
	}
	for _, s := range motionSpecs {
		d.Motion[s.key] = MotionDefinition{new(s.ms), new(s.easing)}
	}
	for _, s := range surfaceSpecs {
		d.Materials[s.key] = SurfaceDefinition{new(None), new("@" + s.background), new(float32(1)), new(float32(0)), new(GlassNone), new(float32(0))}
	}
	return d
}

var light = sync.OnceValue(func() *Theme {
	t, e := compile(Definition{Version: Version, Base: LightBase})
	if e != nil {
		panic(e)
	}
	return t
})
var dark = sync.OnceValue(func() *Theme {
	t, e := compile(Definition{Version: Version, Base: DarkBase})
	if e != nil {
		panic(e)
	}
	return t
})

func Light() *Theme { return light() }
func Dark() *Theme  { return dark() }

func clone(d Definition) Definition {
	d.Colors = maps.Clone(d.Colors)
	d.Metrics = maps.Clone(d.Metrics)
	d.Typography = maps.Clone(d.Typography)
	for k, v := range d.Typography {
		if v.Size != nil {
			v.Size = new(*v.Size)
		}
		if v.Weight != nil {
			v.Weight = new(*v.Weight)
		}
		d.Typography[k] = v
	}
	d.Motion = maps.Clone(d.Motion)
	for k, v := range d.Motion {
		if v.Duration != nil {
			v.Duration = new(*v.Duration)
		}
		if v.Easing != nil {
			v.Easing = new(*v.Easing)
		}
		d.Motion[k] = v
	}
	d.Materials = maps.Clone(d.Materials)
	for k, v := range d.Materials {
		if v.Kind != nil {
			v.Kind = new(*v.Kind)
		}
		if v.Background != nil {
			v.Background = new(*v.Background)
		}
		if v.Alpha != nil {
			v.Alpha = new(*v.Alpha)
		}
		if v.BackdropBlur != nil {
			v.BackdropBlur = new(*v.BackdropBlur)
		}
		if v.Glass != nil {
			v.Glass = new(*v.Glass)
		}
		if v.Clarity != nil {
			v.Clarity = new(*v.Clarity)
		}
		d.Materials[k] = v
	}
	if d.Dark != nil {
		d.Dark = new(*d.Dark)
	}
	if d.Font != nil {
		d.Font = new(*d.Font)
	}
	return d
}
func (t *Theme) Definition() Definition { return clone(t.definition) }
