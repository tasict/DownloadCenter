// mkicons renders the package icons in a coloured-pencil style: a paper
// tile with tooth, a download arrow and tray laid in with overlapping
// hatching in several pencil colours, and wobbly hand-drawn outlines.
// Pigment only catches on the paper's raised grain and layers multiply,
// like real pencil. Usage: go run ./cmd/mkicons <repo root>
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
)

const N = 1024 // working resolution

type canvas struct {
	r, g, b []float64 // 0..1, multiplicative layering
	a       []float64 // tile coverage (alpha)
	tooth   []float64 // paper grain 0..1
}

func newCanvas() *canvas {
	c := &canvas{r: make([]float64, N*N), g: make([]float64, N*N), b: make([]float64, N*N), a: make([]float64, N*N), tooth: make([]float64, N*N)}
	for i := range c.r {
		c.r[i], c.g[i], c.b[i] = 1, 1, 1
	}
	return c
}

// --- noise ---

type noise struct {
	perm [512]int
	grad [256][2]float64
}

func newNoise(seed int64) *noise {
	n := &noise{}
	rnd := rand.New(rand.NewSource(seed))
	p := rnd.Perm(256)
	for i := 0; i < 512; i++ {
		n.perm[i] = p[i&255]
	}
	for i := range n.grad {
		a := rnd.Float64() * 2 * math.Pi
		n.grad[i] = [2]float64{math.Cos(a), math.Sin(a)}
	}
	return n
}

func fade(t float64) float64 { return t * t * t * (t*(t*6-15) + 10) }

func (n *noise) at(x, y float64) float64 {
	xi, yi := int(math.Floor(x))&255, int(math.Floor(y))&255
	xf, yf := x-math.Floor(x), y-math.Floor(y)
	dot := func(ix, iy int, dx, dy float64) float64 {
		g := n.grad[n.perm[n.perm[ix&255]+iy&255]]
		return g[0]*dx + g[1]*dy
	}
	u, v := fade(xf), fade(yf)
	a := dot(xi, yi, xf, yf) + u*(dot(xi+1, yi, xf-1, yf)-dot(xi, yi, xf, yf))
	b := dot(xi, yi+1, xf, yf-1) + u*(dot(xi+1, yi+1, xf-1, yf-1)-dot(xi, yi+1, xf, yf-1))
	return a + v*(b-a) // about -0.7..0.7
}

func (n *noise) fbm(x, y float64, oct int) float64 {
	s, amp, f := 0.0, 0.5, 1.0
	for i := 0; i < oct; i++ {
		s += amp * n.at(x*f, y*f)
		f *= 2.03
		amp *= 0.5
	}
	return s
}

// --- shapes (signed distance, negative inside), in 0..1 units ---

func sdRoundRect(x, y, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	dx, dy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
	ox, oy := math.Max(dx, 0), math.Max(dy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
}

type pt struct{ x, y float64 }

func sdPoly(x, y float64, p []pt) float64 {
	d := math.Inf(1)
	inside := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		ax, ay, bx, by := p[j].x, p[j].y, p[i].x, p[i].y
		ex, ey := bx-ax, by-ay
		wx, wy := x-ax, y-ay
		t := math.Max(0, math.Min(1, (wx*ex+wy*ey)/(ex*ex+ey*ey)))
		d = math.Min(d, math.Hypot(wx-ex*t, wy-ey*t))
		if (ay > y) != (by > y) && x < (bx-ax)*(y-ay)/(by-ay)+ax {
			inside = !inside
		}
	}
	if inside {
		return -d
	}
	return d
}

// The motif: a broad down arrow dropping into an open tray.
var arrow = []pt{{0.405, 0.17}, {0.595, 0.17}, {0.595, 0.445}, {0.725, 0.445}, {0.5, 0.67}, {0.275, 0.445}, {0.405, 0.445}}
var tray = []pt{{0.20, 0.62}, {0.27, 0.62}, {0.27, 0.73}, {0.73, 0.73}, {0.73, 0.62}, {0.80, 0.62}, {0.80, 0.80}, {0.20, 0.80}}

const tileR = 0.225

func tileSD(x, y float64) float64 { return sdRoundRect(x, y, 0.04, 0.04, 0.96, 0.96, tileR) }

// --- pencil ---

type pencil struct {
	r, g, b float64 // pigment colour
	width   float64 // in pixels at N
	press   float64 // 0..1
}

// stroke lays pigment along a polyline. Pigment = coverage x pressure,
// gated by the paper tooth (a soft lead fills more of the grain), and the
// layer multiplies onto what is there. clip limits it to a shape.
func (c *canvas) stroke(pts []pt, p pencil, clip func(x, y float64) float64, rnd *rand.Rand) {
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		ax, ay, bx, by := a.x*N, a.y*N, b.x*N, b.y*N
		w := p.width
		minx, maxx := int(math.Min(ax, bx)-w-2), int(math.Max(ax, bx)+w+2)
		miny, maxy := int(math.Min(ay, by)-w-2), int(math.Max(ay, by)+w+2)
		ex, ey := bx-ax, by-ay
		l2 := ex*ex + ey*ey
		if l2 == 0 {
			continue
		}
		for y := maxInt(miny, 0); y <= minInt(maxy, N-1); y++ {
			for x := maxInt(minx, 0); x <= minInt(maxx, N-1); x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				t := math.Max(0, math.Min(1, ((px-ax)*ex+(py-ay)*ey)/l2))
				d := math.Hypot(px-ax-ex*t, py-ay-ey*t)
				if d > w/2+1 {
					continue
				}
				cov := math.Min(1, math.Max(0, w/2+0.5-d))
				// pressure falls off toward the stroke's edges and ends
				edge := 1 - math.Pow(d/(w/2+1), 2)
				taper := math.Min(1, math.Min(float64(i-1)+t, float64(len(pts)-i)+(1-t))*1.6+0.25)
				if clip != nil {
					sd := clip(px/N, py/N) * N
					if sd > 1.5 {
						continue
					}
					if sd > -1.5 {
						cov *= (1.5 - sd) / 3
					}
				}
				idx := y*N + x
				tooth := c.tooth[idx]
				pr := p.press * edge * taper
				// soft lead fills more of the grain at higher pressure
				grab := smooth(1-pr*1.15, 1-pr*1.15+0.35, tooth)
				amt := cov * grab * (0.55 + 0.45*pr)
				if amt <= 0 {
					continue
				}
				c.r[idx] *= 1 - amt*(1-p.r)
				c.g[idx] *= 1 - amt*(1-p.g)
				c.b[idx] *= 1 - amt*(1-p.b)
			}
		}
	}
}

func smooth(e0, e1, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-e0)/(e1-e0)))
	return t * t * (3 - 2*t)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// hatch fills a shape with parallel strokes at angle (radians), spacing in
// 0..1 units, with jitter so it reads hand made.
func (c *canvas) hatch(sd func(x, y float64) float64, angle, spacing float64, p pencil, rnd *rand.Rand, nz *noise) {
	dx, dy := math.Cos(angle), math.Sin(angle)
	nx, ny := -dy, dx
	for off := -0.8; off <= 0.8; off += spacing * (0.75 + rnd.Float64()*0.5) {
		var pts []pt
		cx, cy := 0.5+nx*off, 0.5+ny*off
		wob := rnd.Float64() * 100
		for s := -0.75; s <= 0.75; s += 0.012 {
			jit := nz.at(wob+s*3, off*7) * 0.004
			pts = append(pts, pt{cx + dx*s + nx*jit, cy + dy*s + ny*jit})
		}
		// keep only the part of the line that runs through the shape (+ a little overshoot)
		var seg []pt
		flush := func() {
			if len(seg) > 2 {
				q := p
				q.press = p.press * (0.75 + rnd.Float64()*0.35)
				q.width = p.width * (0.8 + rnd.Float64()*0.4)
				c.stroke(seg, q, sd, rnd)
			}
			seg = nil
		}
		for _, q := range pts {
			if sd(q.x, q.y) < 0.012 {
				seg = append(seg, q)
			} else {
				flush()
			}
		}
		flush()
	}
}

// outline draws a shape's border a few times with a wobble, like going
// over a line again with a sharp pencil.
func (c *canvas) outline(poly []pt, closed bool, p pencil, passes int, rnd *rand.Rand, nz *noise) {
	for k := 0; k < passes; k++ {
		var pts []pt
		seed := rnd.Float64() * 50
		path := poly
		if closed {
			path = append(append([]pt{}, poly...), poly[0])
		}
		for i := 1; i < len(path); i++ {
			a, b := path[i-1], path[i]
			l := math.Hypot(b.x-a.x, b.y-a.y)
			steps := int(l/0.006) + 1
			for s := 0; s <= steps; s++ {
				t := float64(s) / float64(steps)
				x, y := a.x+(b.x-a.x)*t, a.y+(b.y-a.y)*t
				j := 0.0045
				x += nz.at(seed+x*9, y*9) * j
				y += nz.at(seed+y*9+31, x*9) * j
				pts = append(pts, pt{x, y})
			}
		}
		q := p
		q.press = p.press * (0.8 + rnd.Float64()*0.25)
		c.stroke(pts, q, nil, rnd)
	}
}

func roundRectPath(x0, y0, x1, y1, r float64) []pt {
	var out []pt
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 8; i++ {
			a := a0 + float64(i)/8*math.Pi/2
			out = append(out, pt{cx + math.Cos(a)*r, cy + math.Sin(a)*r})
		}
	}
	corner(x1-r, y1-r, 0)
	corner(x0+r, y1-r, math.Pi/2)
	corner(x0+r, y0+r, math.Pi)
	corner(x1-r, y0+r, 3*math.Pi/2)
	return out
}

func hex(s string) (float64, float64, float64) {
	var v [3]float64
	for i := 0; i < 3; i++ {
		var b int
		for _, ch := range s[1+2*i : 3+2*i] {
			b *= 16
			switch {
			case ch >= '0' && ch <= '9':
				b += int(ch - '0')
			default:
				b += int(ch-'A') + 10
			}
		}
		v[i] = float64(b) / 255
	}
	return v[0], v[1], v[2]
}

func pen(c string, w, press float64) pencil {
	r, g, b := hex(c)
	return pencil{r, g, b, w, press}
}

func render(maskable bool) *canvas {
	c := newCanvas()
	rnd := rand.New(rand.NewSource(7))
	grain, warp := newNoise(11), newNoise(23)
	// Paper: warm white with fibres; tooth = fine grain the lead catches on
	pr, pg, pb := hex("#FFFDF8")
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			fx, fy := float64(x)/N, float64(y)/N
			i := y*N + x
			t := 0.5 + 0.9*grain.fbm(fx*190, fy*190, 3) + 0.25*grain.at(fx*35+9, fy*35)
			c.tooth[i] = math.Max(0, math.Min(1, t))
			sd := tileSD(fx, fy)
			if maskable {
				sd = -1
			}
			c.a[i] = math.Max(0, math.Min(1, 0.5-sd*N))
			fib := 1 - 0.035*math.Max(0, warp.fbm(fx*14, fy*60, 3))
			c.r[i], c.g[i], c.b[i] = pr*fib, pg*fib, pb*fib
		}
	}
	tileClip := func(x, y float64) float64 {
		if maskable {
			return -1
		}
		return tileSD(x, y) + 0.004
	}
	// The original mark: a white down arrow over a tray bar, cut out of a blue-to-violet tile. Here the tile is laid in with
	// coloured pencil and the mark is the paper left bare.
	shaftSD := func(x, y float64) float64 { return sdRoundRect(x, y, 0.445, 0.20, 0.555, 0.56, 0.05) }
	headSD := func(x, y float64) float64 { return sdPoly(x, y, []pt{{0.27, 0.45}, {0.73, 0.45}, {0.50, 0.68}}) }
	barSD := func(x, y float64) float64 { return sdRoundRect(x, y, 0.25, 0.74, 0.75, 0.82, 0.04) }
	markSD := func(x, y float64) float64 { return math.Min(shaftSD(x, y), math.Min(headSD(x, y), barSD(x, y))) }
	// Tile minus the mark, with a small paper gap around the mark so its edge stays crisp
	bgSD := func(x, y float64) float64 { return math.Max(tileClip(x, y), 0.007-markSD(x, y)) }
	// Vertical colour bands with ragged edges: sky blue at the top, azure, then violet at the bottom (the original gradient)
	band := func(y0, y1 float64) func(x, y float64) float64 {
		return func(x, y float64) float64 {
			yy := y + warp.at(x*7, y*7)*0.07
			return math.Max(bgSD(x, y), math.Max(y0-yy, yy-y1))
		}
	}
	// The gradient as eight bands that meet on the same ragged edge (no overlap, so no dark seam): each band is
	// one pencil colour stepped from azure to blue-violet, then a lighter crosshatch stepped the same way
	lerp := func(a, b string, t float64) pencil {
		ar, ag, ab := hex(a)
		br, bg, bb := hex(b)
		return pencil{ar + (br-ar)*t, ag + (bg-ag)*t, ab + (bb-ab)*t, 11, 0.78}
	}
	for i := 0; i < 8; i++ {
		y0, y1 := 0.12+float64(i)*0.1, 0.12+float64(i+1)*0.1
		if i == 0 {
			y0 = -1
		}
		if i == 7 {
			y1 = 2
		}
		t := float64(i) / 7
		c.hatch(band(y0, y1), -0.95, 0.0080, lerp("#2F8DF7", "#6F5CEB", t), rnd, warp)
		l := lerp("#6CC2FF", "#A07CF5", t)
		l.width, l.press = 10, 0.5
		c.hatch(band(y0, y1), 0.55, 0.0100, l, rnd, warp)
	}
	// Top highlight of the original: a lighter pass in the upper part, almost white so it lifts the blue
	c.hatch(band(-1, 0.22), -0.2, 0.014, pen("#BFE2FF", 9, 0.35), rnd, warp)
	// The mark: paper, with a faint pencil shadow on its lower right to give it body
	shade := func(x, y float64) float64 { return math.Max(markSD(x, y)+0.004, 0.53-x+(y-0.5)*0.2) }
	c.hatch(shade, -0.9, 0.012, pen("#DCE6FA", 7, 0.32), rnd, warp)
	// Edges of the mark gone over lightly with a deep blue pencil, so it reads at small sizes
	edge := pen("#2C4FB8", 6, 0.42)
	// The arrow as one outline (shaft and head together), so no line runs across the join
	arrowPath := []pt{}
	for i := 0; i <= 12; i++ {
		a := math.Pi + float64(i)/12*math.Pi
		arrowPath = append(arrowPath, pt{0.5 + math.Cos(a)*0.055, 0.255 + math.Sin(a)*0.055})
	}
	arrowPath = append(arrowPath, pt{0.555, 0.45}, pt{0.73, 0.45}, pt{0.50, 0.68}, pt{0.27, 0.45}, pt{0.445, 0.45})
	c.outline(arrowPath, true, edge, 1, rnd, warp)
	c.outline(roundRectPath(0.25, 0.74, 0.75, 0.82, 0.04), true, edge, 1, rnd, warp)
	if !maskable {
		c.outline(roundRectPath(0.04, 0.04, 0.96, 0.96, tileR), true, pen("#3E5FC9", 7, 0.5), 1, rnd, warp)
	}
	return c
}

// sample box-filters the working canvas down to size x size.
func (c *canvas) sample(size int, gray bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	f := float64(N) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a, n float64
			for sy := int(float64(y) * f); sy < int(float64(y+1)*f); sy++ {
				for sx := int(float64(x) * f); sx < int(float64(x+1)*f); sx++ {
					i := sy*N + sx
					w := c.a[i]
					r += c.r[i] * w
					g += c.g[i] * w
					b += c.b[i] * w
					a += w
					n++
				}
			}
			if a == 0 {
				continue
			}
			r, g, b = r/a, g/a, b/a
			if gray {
				l := 0.3*r + 0.59*g + 0.11*b
				r, g, b = l, l, l
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r*255 + 0.5), uint8(g*255 + 0.5), uint8(b*255 + 0.5), uint8(a/n*255 + 0.5)})
		}
	}
	return img
}

func save(p string, img image.Image) {
	os.MkdirAll(filepath.Dir(p), 0755)
	f, err := os.Create(p)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	tile, full := render(false), render(true)
	save(filepath.Join(root, "icons", "DownloadCenter.png"), tile.sample(64, false))
	save(filepath.Join(root, "icons", "DownloadCenter_80.png"), tile.sample(80, false))
	save(filepath.Join(root, "icons", "DownloadCenter_gray.png"), tile.sample(64, true))
	save(filepath.Join(root, "shared", ".qpkg_icon_256.png"), tile.sample(256, false))
	save(filepath.Join(root, "shared", "web", "img", "logo.png"), tile.sample(256, false))
	save(filepath.Join(root, "shared", "web", "img", "icon-192.png"), tile.sample(192, false))
	save(filepath.Join(root, "shared", "web", "img", "icon-512.png"), tile.sample(512, false))
	save(filepath.Join(root, "shared", "web", "img", "icon-maskable-512.png"), full.sample(512, false))
	save(filepath.Join(root, "shared", "web", "img", "apple-touch-icon.png"), full.sample(256, false))
	save(filepath.Join(root, "shared", "web", "img", "favicon-32.png"), tile.sample(32, false))
}
