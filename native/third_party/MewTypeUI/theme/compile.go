package theme

import (
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// Only the most recent edit is retained: an editor can compile indefinitely without growing a global cache.
var compiledCache struct {
	sync.Mutex
	input Definition
	theme *Theme
}

func Compile(d Definition) (*Theme, error) {
	compiledCache.Lock()
	defer compiledCache.Unlock()
	if compiledCache.theme != nil && reflect.DeepEqual(d, compiledCache.input) {
		return compiledCache.theme, nil
	}
	t, e := compile(d)
	if e != nil {
		return nil, e
	}
	compiledCache.input = clone(d)
	compiledCache.theme = t
	return t, nil
}

func compile(d Definition) (*Theme, error) {
	if d.Version != Version {
		return nil, fmt.Errorf("version: unsupported %d (want %d)", d.Version, Version)
	}
	if d.Base != "" && d.Base != LightBase && d.Base != DarkBase {
		return nil, fmt.Errorf("base: unknown %q", d.Base)
	}
	if e := knownKeys(d); e != nil {
		return nil, e
	}
	var full Definition
	if d.Base == "" {
		full = clone(d)
	}
	if d.Base != "" {
		full = preset(d.Base)
		if d.Dark != nil {
			full.Dark = new(*d.Dark)
		}
		if d.Font != nil {
			full.Font = new(*d.Font)
		}
		for k, v := range d.Colors {
			full.Colors[k] = v
		}
		for k, v := range d.Metrics {
			full.Metrics[k] = v
		}
		for k, v := range d.Typography {
			s := full.Typography[k]
			if v.Size != nil {
				s.Size = new(*v.Size)
			}
			if v.Weight != nil {
				s.Weight = new(*v.Weight)
			}
			full.Typography[k] = s
		}
		for k, v := range d.Motion {
			s := full.Motion[k]
			if v.Duration != nil {
				s.Duration = new(*v.Duration)
			}
			if v.Easing != nil {
				s.Easing = new(*v.Easing)
			}
			full.Motion[k] = s
		}
		for k, v := range d.Materials {
			s := full.Materials[k]
			if v.Kind != nil {
				s.Kind = new(*v.Kind)
			}
			if v.Background != nil {
				s.Background = new(*v.Background)
			}
			if v.Alpha != nil {
				s.Alpha = new(*v.Alpha)
			}
			if v.BackdropBlur != nil {
				s.BackdropBlur = new(*v.BackdropBlur)
			}
			if v.Glass != nil {
				s.Glass = new(*v.Glass)
			}
			if v.Clarity != nil {
				s.Clarity = new(*v.Clarity)
			}
			full.Materials[k] = s
		}
	}
	if full.Dark == nil {
		return nil, fmt.Errorf("dark: missing value")
	}
	if full.Font == nil {
		return nil, fmt.Errorf("font: missing value")
	}
	t := &Theme{dark: *full.Dark, definition: full}
	t.typography.font = *full.Font
	resolved := map[string]ui.Color{}
	visiting := map[string]bool{}
	var resolve func(string) (ui.Color, error)
	resolve = func(k string) (ui.Color, error) {
		if c, ok := resolved[k]; ok {
			return c, nil
		}
		v, ok := full.Colors[k]
		if !ok {
			return ui.Color{}, fmt.Errorf("colors.%s: missing reference", k)
		}
		if visiting[k] {
			return ui.Color{}, fmt.Errorf("colors.%s: cyclic reference", k)
		}
		visiting[k] = true
		var c ui.Color
		var e error
		if strings.HasPrefix(v, "@") {
			c, e = resolve(v[1:])
			if e != nil {
				return c, fmt.Errorf("colors.%s -> %w", k, e)
			}
		} else {
			c, e = parseColor(v)
			if e != nil {
				return c, fmt.Errorf("colors.%s: %w", k, e)
			}
		}
		delete(visiting, k)
		resolved[k] = c
		return c, nil
	}
	for i, s := range colorSpecs {
		c, e := resolve(s.key)
		if e != nil {
			return nil, e
		}
		t.colors.values[i] = c
	}
	for i, s := range metricSpecs {
		v, ok := full.Metrics[s.key]
		if !ok {
			return nil, fmt.Errorf("metrics.%s: missing value", s.key)
		}
		if !finite(float64(v)) || v < 0 {
			return nil, fmt.Errorf("metrics.%s: must be finite and nonnegative", s.key)
		}
		t.metrics.values[i] = v
	}
	for i, s := range textSpecs {
		v, ok := full.Typography[s.key]
		if !ok {
			return nil, fmt.Errorf("typography.%s: missing value", s.key)
		}
		if v.Size == nil {
			return nil, fmt.Errorf("typography.%s.size: missing value", s.key)
		}
		if v.Weight == nil {
			return nil, fmt.Errorf("typography.%s.weight: missing value", s.key)
		}
		if !finite(float64(*v.Size)) || *v.Size <= 0 {
			return nil, fmt.Errorf("typography.%s.size: must be finite and positive", s.key)
		}
		if *v.Weight < 1 || *v.Weight > 1000 {
			return nil, fmt.Errorf("typography.%s.weight: want 1..1000", s.key)
		}
		t.typography.styles[i] = TextStyle{*v.Size, *v.Weight}
	}
	for i, s := range motionSpecs {
		v, ok := full.Motion[s.key]
		if !ok {
			return nil, fmt.Errorf("motion.%s: missing value", s.key)
		}
		if v.Duration == nil {
			return nil, fmt.Errorf("motion.%s.duration: missing value", s.key)
		}
		if v.Easing == nil {
			return nil, fmt.Errorf("motion.%s.easing: missing value", s.key)
		}
		if !finite(*v.Duration) || *v.Duration < 0 || *v.Duration >= float64(math.MaxInt64)/float64(time.Millisecond) {
			return nil, fmt.Errorf("motion.%s.duration: invalid milliseconds", s.key)
		}
		switch *v.Easing {
		case Linear, EaseIn, EaseOut, EaseInOut, EaseOutCirc, EaseOutQuint, SineInOut:
		default:
			return nil, fmt.Errorf("motion.%s.easing: unknown %q", s.key, *v.Easing)
		}
		t.motion.transitions[i] = Transition{time.Duration(*v.Duration * float64(time.Millisecond)), *v.Easing}
	}
	for i, s := range surfaceSpecs {
		v, ok := full.Materials[s.key]
		path := "materials." + s.key
		if !ok {
			return nil, fmt.Errorf("%s: missing surface", path)
		}
		if v.Kind == nil {
			return nil, fmt.Errorf("%s.kind: missing value", path)
		}
		if v.Background == nil {
			return nil, fmt.Errorf("%s.background: missing value", path)
		}
		if v.Alpha == nil {
			return nil, fmt.Errorf("%s.alpha: missing value", path)
		}
		if v.BackdropBlur == nil {
			return nil, fmt.Errorf("%s.backdropBlur: missing value", path)
		}
		if v.Glass == nil {
			return nil, fmt.Errorf("%s.glass: missing value", path)
		}
		if v.Clarity == nil {
			return nil, fmt.Errorf("%s.clarity: missing value", path)
		}
		switch *v.Kind {
		case None, Mica, Acrylic:
		default:
			return nil, fmt.Errorf("%s.kind: unknown %q", path, *v.Kind)
		}
		if s.key != "window" && *v.Kind != None {
			return nil, fmt.Errorf("%s.kind: system material belongs to window", path)
		}
		if !unit(*v.Alpha) {
			return nil, fmt.Errorf("%s.alpha: want 0..1", path)
		}
		if !unit(*v.Clarity) {
			return nil, fmt.Errorf("%s.clarity: want 0..1", path)
		}
		if !finite(float64(*v.BackdropBlur)) || *v.BackdropBlur < 0 {
			return nil, fmt.Errorf("%s.backdropBlur: must be finite and nonnegative", path)
		}
		switch *v.Glass {
		case GlassNone, GlassRegular, GlassClear:
		default:
			return nil, fmt.Errorf("%s.glass: unknown %q", path, *v.Glass)
		}
		var c ui.Color
		var e error
		if strings.HasPrefix(*v.Background, "@") {
			c, e = resolve((*v.Background)[1:])
		} else {
			c, e = parseColor(*v.Background)
		}
		if e != nil {
			return nil, fmt.Errorf("%s.background: %w", path, e)
		}
		t.materials.surfaces[i] = Surface{*v.Kind, c, *v.Alpha, *v.BackdropBlur, *v.Clarity, *v.Glass}
	}
	return t, nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func unit(v float32) bool   { return finite(float64(v)) && v >= 0 && v <= 1 }
func parseColor(v string) (ui.Color, error) {
	if (len(v) != 7 && len(v) != 9) || v[0] != '#' {
		return ui.Color{}, fmt.Errorf("invalid color %q; want #RRGGBB or #RRGGBBAA", v)
	}
	var data [4]byte
	data[3] = 255
	_, e := hex.Decode(data[:], []byte(v[1:]))
	if e != nil {
		return ui.Color{}, fmt.Errorf("invalid color %q", v)
	}
	return ui.Color{R: data[0], G: data[1], B: data[2], A: data[3]}, nil
}

type tokenKeys struct{ colors, metrics, texts, motions, surfaces map[string]bool }

var known = func() tokenKeys {
	k := tokenKeys{
		colors: make(map[string]bool, len(colorSpecs)), metrics: make(map[string]bool, len(metricSpecs)),
		texts: make(map[string]bool, len(textSpecs)), motions: make(map[string]bool, len(motionSpecs)), surfaces: make(map[string]bool, len(surfaceSpecs)),
	}
	for _, s := range colorSpecs {
		k.colors[s.key] = true
	}
	for _, s := range metricSpecs {
		k.metrics[s.key] = true
	}
	for _, s := range textSpecs {
		k.texts[s.key] = true
	}
	for _, s := range motionSpecs {
		k.motions[s.key] = true
	}
	for _, s := range surfaceSpecs {
		k.surfaces[s.key] = true
	}
	return k
}()

func knownKeys(d Definition) error {
	for k := range d.Colors {
		if !known.colors[k] {
			return fmt.Errorf("colors.%s: unknown key", k)
		}
	}
	for k := range d.Metrics {
		if !known.metrics[k] {
			return fmt.Errorf("metrics.%s: unknown key", k)
		}
	}
	for k := range d.Typography {
		if !known.texts[k] {
			return fmt.Errorf("typography.%s: unknown key", k)
		}
	}
	for k := range d.Motion {
		if !known.motions[k] {
			return fmt.Errorf("motion.%s: unknown key", k)
		}
	}
	for k := range d.Materials {
		if !known.surfaces[k] {
			return fmt.Errorf("materials.%s: unknown key", k)
		}
	}
	return nil
}
