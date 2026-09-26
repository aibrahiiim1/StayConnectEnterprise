package main

// THE HOTEL'S COLOURS, MADE READABLE.
//
// A hotel picks a brand colour, a darker shade and a text colour. Each of them ends up behind or in front of
// words a guest has to read: the Login label sits on the brand colour, the same label sits on the darker shade
// while a finger is on it, links are drawn in the darker shade, and the page's text is drawn in the text
// colour. None of the three is checked when it is chosen, so the page checks at render time and quietly
// corrects what would be unreadable -- a yellow brand gets dark button text, a pale link shade is darkened,
// a pale text colour gives way to the portal's own ink. Nothing here refuses a design; it only derives the
// values the stylesheet reads (--sc-on-brand, --sc-on-brand-dark, --sc-link, --sc-on-hero, --sc-hero-shade).
//
// The sign-in page's script carries the same derivation (deriveColours in templates.go), because the admin
// preview hands it an unsaved design that never passes through this file. The two must agree; the tests in
// portal_colour_test.go pin this side, and the script is a line-for-line port of it.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The portal's own values (portal_css.go), which a design that leaves a colour unset falls back to.
const (
	defaultBrand     = "#1773bd"
	defaultBrandDark = "#125c97"
	inkDark          = "#14161a"
	// minTextContrast is WCAG AA for body text. The button label is 16px/600, which is not "large" text, so
	// the 3:1 the old check used was not enough.
	minTextContrast = 4.5
	// minPhotoOverlay is the least darkening a photograph gets under the hero's white text: a hotel that set
	// 0% would otherwise put white words straight onto a bright sky.
	minPhotoOverlay = 0.30
	// defaultOverlay is --sc-overlay's value in portal_css.go.
	defaultOverlay = 0.35
)

type colour struct{ r, g, b, a float64 } // channels 0..255, alpha 0..1

var (
	white   = colour{255, 255, 255, 1}
	black   = colour{0, 0, 0, 1}
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

// inkOn answers which text colour belongs on the opaque surfaces given: white when it reads on all of them,
// otherwise whichever of white and the dark ink reads better on the worst of them. The answer is "" for white
// (the stylesheet's default) and the dark ink otherwise.
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

// derivedColours is what the stylesheet needs beyond the colours the hotel chose, as custom properties in a
// fixed order; a property the defaults already get right is left out. overlay is the photo darkening the
// page will use (the stylesheet's default when the design sets none), photo whether any photograph is shown.
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
	// With no photograph, the hero's words sit on the brand gradient under the darkening; the ink is chosen for
	// both ends of it. A photograph keeps white, over at least minPhotoOverlay of darkening.
	if !photo && (bOK || bdOK) {
		if ink := inkOn(shade(b, overlay), shade(bd, overlay)); ink != "" {
			set("--sc-on-hero", ink)
			set("--sc-hero-shade", "transparent")
		}
	}
	return out
}
