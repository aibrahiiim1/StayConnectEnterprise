package main

// Hotel colours are not checked when chosen, so they are corrected at render time (never refused): this
// derives --sc-on-brand, --sc-on-brand-dark, --sc-link, --sc-on-hero and --sc-hero-shade. The sign-in page's
// script ports this line for line (deriveColours in templates.go) for unsaved admin previews; keep them in
// agreement.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Defaults from portal_css.go, used when a design leaves a colour unset.
const (
	defaultBrand     = "#1773bd"
	defaultBrandDark = "#125c97"
	inkDark          = "#14161a"
	// minTextContrast is WCAG AA for body text; the 16px/600 button label is not "large" text.
	minTextContrast = 4.5
	// minPhotoOverlay is the least photo darkening under the hero's white text, sized for a white photograph:
	// white text reaches 4.5:1 from alpha 0.54, and the 0.95-opacity welcome line needs 0.56.
	minPhotoOverlay = 0.56
	// defaultOverlay is --sc-overlay's value in portal_css.go.
	defaultOverlay = 0.35
)

type colour struct{ r, g, b, a float64 } // channels 0..255, alpha 0..1

var (
	white   = colour{255, 255, 255, 1}
	reRGBFn = regexp.MustCompile(`(?i)^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*(?:,\s*([0-9]*\.?[0-9]+)(%?)\s*)?\)$`)
)

// parseColour reads the forms a design may carry: #rgb, #rgba, #rrggbb, #rrggbbaa, rgb() and rgba().
func parseColour(s string) (colour, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		h := s[1:]
		if len(h) == 3 || len(h) == 4 {
			var b strings.Builder
			for i := 0; i < len(h); i++ {
				b.WriteByte(h[i])
				b.WriteByte(h[i])
			}
			h = b.String()
		}
		if len(h) != 6 && len(h) != 8 {
			return colour{}, false
		}
		v, err := strconv.ParseUint(h, 16, 64)
		if err != nil {
			return colour{}, false
		}
		c := colour{a: 1}
		if len(h) == 8 {
			c.a = float64(v&0xff) / 255
			v >>= 8
		}
		c.r, c.g, c.b = float64(v>>16&0xff), float64(v>>8&0xff), float64(v&0xff)
		return c, true
	}
	m := reRGBFn.FindStringSubmatch(s)
	if m == nil {
		return colour{}, false
	}
	ch := func(t string) float64 { n, _ := strconv.Atoi(t); return math.Min(255, float64(n)) }
	c := colour{ch(m[1]), ch(m[2]), ch(m[3]), 1}
	if m[4] != "" {
		a, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return colour{}, false
		}
		if m[5] == "%" {
			a /= 100
		}
		c.a = math.Min(1, math.Max(0, a))
	}
	return c, true
}

// over is c painted on an opaque bg.
func over(c, bg colour) colour {
	return colour{c.r*c.a + bg.r*(1-c.a), c.g*c.a + bg.g*(1-c.a), c.b*c.a + bg.b*(1-c.a), 1}
}

// shade is c under a black layer of alpha k -- the hero's darkening.
func shade(c colour, k float64) colour { return over(colour{0, 0, 0, k}, c) }

func luminance(c colour) float64 {
	lin := func(v float64) float64 {
		f := v / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

func contrast(a, b colour) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func hex(c colour) string {
	r := func(v float64) int { return int(math.Round(math.Max(0, math.Min(255, v)))) }
	return fmt.Sprintf("#%02x%02x%02x", r(c.r), r(c.g), r(c.b))
}

// inkOn returns "" for white (the stylesheet default) when it reads on all surfaces, else the better of white
// and the dark ink on the worst surface.
func inkOn(surfaces ...colour) string {
	worst := func(ink colour) float64 {
		m := math.Inf(1)
		for _, s := range surfaces {
			m = math.Min(m, contrast(ink, s))
		}
		return m
	}
	w, d := worst(white), worst(mustColour(inkDark))
	if w >= minTextContrast || w >= d {
		return ""
	}
	return inkDark
}

func mustColour(s string) colour { c, _ := parseColour(s); return c }

// readableOnWhite darkens c until it reads as text on the white card; "" when it already does.
func readableOnWhite(c colour) string {
	c = over(c, white)
	if contrast(c, white) >= minTextContrast {
		return ""
	}
	for f := 0.95; f > 0; f -= 0.05 {
		// Judged as written: the colour the page receives is the rounded hex.
		d := hex(colour{c.r * f, c.g * f, c.b * f, 1})
		if contrast(mustColour(d), white) >= minTextContrast {
			return d
		}
	}
	return inkDark
}

// derivedColours returns custom properties in a fixed order, omitting those the defaults already get right.
// overlay is the photo darkening in use; photo is whether any photograph is shown.
func derivedColours(brand, brandDark, text string, overlay float64, photo bool) [][2]string {
	var out [][2]string
	set := func(prop, v string) {
		if v != "" {
			out = append(out, [2]string{prop, v})
		}
	}
	b, bOK := parseColour(brand)
	if !bOK {
		b = mustColour(defaultBrand)
	}
	bd, bdOK := parseColour(brandDark)
	if !bdOK {
		bd = mustColour(defaultBrandDark)
	}
	b, bd = over(b, white), over(bd, white)
	if bOK {
		set("--sc-on-brand", inkOn(b))
	}
	if bOK || bdOK {
		set("--sc-on-brand-dark", inkOn(bd))
	}
	if bdOK {
		set("--sc-link", readableOnWhite(bd))
	}
	if t, ok := parseColour(text); ok && contrast(over(t, white), white) >= minTextContrast {
		set("--sc-ink", text)
	}
	// Without a photograph the ink is chosen for both ends of the gradient; a photograph keeps white.
	if !photo && (bOK || bdOK) {
		if ink := inkOn(shade(b, overlay), shade(bd, overlay)); ink != "" {
			set("--sc-on-hero", ink)
			set("--sc-hero-shade", "transparent")
		}
	}
	return out
}
