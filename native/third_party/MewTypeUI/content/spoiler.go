package content

import (
	"image"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// The upstream descriptor describes distributions and a periodic 60-frame tile,
// not reusable artwork. Generate our own white mask once, then tint at paint.
type spoilerRecipe struct {
	size, sprites, particles, frames int
	minimum, maximum, slow, fast     float32
	fadeIn, hold, fadeOut, frame     time.Duration
}
type spoilerAtlas struct {
	recipe spoilerRecipe
	image  *ui.Bitmap
}

var spoilerCache struct {
	sync.Mutex
	atlases [2]*spoilerAtlas
}

func spoilerTexture(t *theme.Theme, imageMask bool) *spoilerAtlas {
	m := t.Metrics().Spoiler()
	motion := t.Motion()
	recipe := spoilerRecipe{int(m.CanvasSize()), int(m.Sprites()), int(m.Particles()), int(m.Frames()), m.SizeMin(), m.SizeMax(), m.SpeedMin(), m.SpeedMax(), motion.SpoilerFadeIn().Duration(), motion.SpoilerHold().Duration(), motion.SpoilerFadeOut().Duration(), motion.SpoilerFrame().Duration()}
	index := 0
	if imageMask {
		index = 1
		recipe.particles = int(m.ImageParticles())
		recipe.slow, recipe.fast = m.ImageSpeedMin(), m.ImageSpeedMax()
		recipe.fadeIn, recipe.hold, recipe.fadeOut = motion.SpoilerImageFadeIn().Duration(), 0, motion.SpoilerImageFadeOut().Duration()
	}
	spoilerCache.Lock()
	defer spoilerCache.Unlock()
	if cached := spoilerCache.atlases[index]; cached != nil && cached.recipe == recipe {
		return cached
	}
	atlas := &spoilerAtlas{recipe: recipe}
	atlas.image = buildSpoiler(recipe)
	spoilerCache.atlases[index] = atlas
	return atlas
}

type spoilerParticle struct {
	birth        int64
	x, y, sprite int
	dx, dy       float64
}

func buildSpoiler(r spoilerRecipe) *ui.Bitmap {
	// Explicit zero overrides produce a static, transparent mask.
	if r.size == 0 || r.frames == 0 || r.particles == 0 || r.sprites == 0 {
		return ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	}
	duration := r.fadeIn + r.hold + r.fadeOut
	if duration == 0 {
		return ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, r.size, r.size)))
	}
	lifetime := duration.Nanoseconds()
	period := int64(r.frames) * r.frame.Nanoseconds()
	if period == 0 {
		period = lifetime + 1
	}
	particles := make([]spoilerParticle, r.particles)
	random := rand.New(rand.NewPCG(1, 2))
	for i := range particles {
		value := random.IntN(2*r.particles + 2)
		sign := 1.
		module := value
		if value < r.particles+1 {
			sign = -1
		} else {
			module -= r.particles + 1
		}
		speed := float64(r.slow) + (float64(r.fast-r.slow)*float64(module))/float64(r.particles)
		divisions := max(1, int(math.Ceil(float64(r.fast)*float64(duration/time.Millisecond))))
		x := float64(random.IntN(2*divisions+1)-divisions) / float64(divisions)
		particles[i] = spoilerParticle{int64(i) * period / int64(r.particles), random.IntN(r.size), random.IntN(r.size), random.IntN(r.sprites), speed * x / float64(lifetime), speed * math.Sqrt(1-x*x) * sign / float64(lifetime)}
	}
	spriteSide := 2 + int(math.Ceil(float64(r.maximum)))
	sprites := make([][]uint8, r.sprites)
	for i := range sprites {
		w, h := float64(r.minimum), float64(r.minimum)
		mid := r.sprites / 2
		if i < mid {
			w += float64(r.maximum-r.minimum) * float64(mid-i) / float64(mid)
		}
		if i > mid {
			h += float64(r.maximum-r.minimum) * float64(i-mid) / float64(r.sprites-1-mid)
		}
		radius := float64(r.minimum) / 2
		sprites[i] = make([]uint8, spriteSide*spriteSide)
		// Four samples per axis approximate platform rounded-rectangle coverage.
		for y := range spriteSide {
			for x := range spriteSide {
				covered := 0
				for sy := range 4 {
					for sx := range 4 {
						px := float64(x) + (float64(sx)+.5)/4
						py := float64(y) + (float64(sy)+.5)/4
						if px < 1 || px > 1+w || py < 1 || py > 1+h {
							continue
						}
						dx := math.Max(math.Max(1+radius-px, px-(1+w-radius)), 0)
						dy := math.Max(math.Max(1+radius-py, py-(1+h-radius)), 0)
						if dx*dx+dy*dy <= radius*radius {
							covered++
						}
					}
				}
				sprites[i][y*spriteSide+x] = uint8(covered * 255 / 16)
			}
		}
	}
	// Vertical packing avoids a fixed atlas column convention in the component.
	sheet := image.NewRGBA(image.Rect(0, 0, r.size, r.size*r.frames))
	for frame := range r.frames {
		now := int64(frame) * r.frame.Nanoseconds()
		for _, particle := range particles {
			age := (now - particle.birth + period) % period
			if age <= 0 || age >= lifetime {
				continue
			}
			opacity := 1.
			if age < r.fadeIn.Nanoseconds() {
				opacity = float64(age) / float64(r.fadeIn)
			} else if age > lifetime-r.fadeOut.Nanoseconds() {
				opacity = float64(lifetime-age) / float64(r.fadeOut)
			}
			x := particle.x + int(math.Round(float64(age)*particle.dx))
			y := particle.y + int(math.Round(float64(age)*particle.dy))
			for py := range spriteSide {
				for px := range spriteSide {
					alpha := int(float64(sprites[particle.sprite][py*spriteSide+px]) * opacity)
					if alpha == 0 {
						continue
					}
					tx, ty := ((x+px)%r.size+r.size)%r.size, ((y+py)%r.size+r.size)%r.size
					at := 4 * ((frame*r.size+ty)*r.size + tx)
					value := sheet.Pix[at+3]
					value += uint8(alpha * (255 - int(value)) / 255)
					sheet.Pix[at], sheet.Pix[at+1], sheet.Pix[at+2], sheet.Pix[at+3] = value, value, value, value
				}
			}
		}
	}
	return ui.NewBitmap(sheet)
}

func (a *spoilerAtlas) draw(p *ui.Painter, r ui.Rect, color ui.Color, reduced bool) {
	if a.recipe.size == 0 || a.recipe.frames == 0 {
		return
	}
	frame := 0
	if !reduced && a.recipe.frame > 0 {
		frame = int(p.Now().UnixNano() / int64(a.recipe.frame) % int64(a.recipe.frames))
		if frame < 0 {
			frame += a.recipe.frames
		}
		p.AnimationFrame()
	}
	size := float32(a.recipe.size)
	p.Clip(r, 0, func() {
		for y := r.Y; y < r.Y+r.H; {
			sy := float32(math.Mod(float64(y), float64(size)))
			if sy < 0 {
				sy += size
			}
			h := min(size-sy, r.Y+r.H-y)
			for x := r.X; x < r.X+r.W; {
				sx := float32(math.Mod(float64(x), float64(size)))
				if sx < 0 {
					sx += size
				}
				w := min(size-sx, r.X+r.W-x)
				p.ImageRegion(a.image, ui.Rect{X: sx, Y: float32(frame*a.recipe.size) + sy, W: w, H: h}, ui.Rect{X: x, Y: y, W: w, H: h}, ui.ImageOptions{Tint: color})
				x += w
			}
			y += h
		}
	})
}
