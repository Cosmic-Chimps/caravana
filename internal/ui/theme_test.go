package ui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests keep the rules of the light theme (feature 028, contract
// specs/028-light-theme/contracts/light-theme.md): every color is a token in
// theme.css with a dark and a light value, no class writes a color of its own,
// and the contract's contrast ratios hold for both palettes. They read the
// style files from disk, since the stylesheet is generated from them and only
// its output is embedded.

// readStyle reads a file of the style folder; the tests run in internal/ui.
func readStyle(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("style", name))
	if err != nil {
		t.Fatalf("read style/%s: %v", name, err)
	}
	return string(b)
}

// ---- The color math, a port of the contract's cr.py ----

// linearRGB is a color in linear sRGB, each channel clamped to 0..1.
type linearRGB struct{ r, g, b float64 }

// color is an oklch color as the tokens write it: its linear sRGB and its alpha.
type color struct {
	rgb   linearRGB
	alpha float64
}

func clamp01(x float64) float64 { return math.Min(1, math.Max(0, x)) }

// oklchToLinear converts oklch(L C h) to linear sRGB with Björn Ottosson's
// matrices, the same numbers as the contract's cr.py.
func oklchToLinear(l, c, hueDeg float64) linearRGB {
	hue := hueDeg * math.Pi / 180
	a := c * math.Cos(hue)
	b := c * math.Sin(hue)
	l1 := l + 0.3963377774*a + 0.2158037573*b
	m1 := l - 0.1055613458*a - 0.0638541728*b
	s1 := l - 0.0894841775*a - 1.2914855480*b
	lc, mc, sc := l1*l1*l1, m1*m1*m1, s1*s1*s1
	return linearRGB{
		r: clamp01(4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc),
		g: clamp01(-1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc),
		b: clamp01(-0.0041960863*lc - 0.7034186147*mc + 1.7076147010*sc),
	}
}

// luminance is WCAG's relative luminance of a linear sRGB color.
func (c linearRGB) luminance() float64 { return 0.2126*c.r + 0.7152*c.g + 0.0722*c.b }

// over blends fg at the given alpha over an opaque ground, in linear sRGB,
// as cr.py's blend does.
func (fg linearRGB) over(alpha float64, ground linearRGB) linearRGB {
	mix := func(f, g float64) float64 { return alpha*f + (1-alpha)*g }
	return linearRGB{mix(fg.r, ground.r), mix(fg.g, ground.g), mix(fg.b, ground.b)}
}

// contrastRatio is WCAG's (lighter + 0.05) / (darker + 0.05).
func contrastRatio(a, b float64) float64 {
	hi, lo := math.Max(a, b), math.Min(a, b)
	return (hi + 0.05) / (lo + 0.05)
}

// contrastOn is the ratio of fg on an opaque ground; an alpha color is
// blended over the ground first.
func contrastOn(fg, ground color) float64 {
	return contrastRatio(fg.rgb.over(fg.alpha, ground.rgb).luminance(), ground.rgb.luminance())
}

func TestContrastMath(t *testing.T) {
	cases := []struct {
		name   string
		fg, bg [3]float64 // L, C, h
		want   float64
	}{
		{"white on black", [3]float64{1, 0, 0}, [3]float64{0, 0, 0}, 21},
		{"same color", [3]float64{0.5, 0.1, 100}, [3]float64{0.5, 0.1, 100}, 1},
		// The contract's table, "Measured on the panel (ink-1)".
		{"dark fg-1 on ink-1", [3]float64{0.955, 0.004, 255}, [3]float64{0.195, 0.007, 255}, 16.0},
		{"light fg-1 on ink-1", [3]float64{0.215, 0.010, 255}, [3]float64{0.995, 0.002, 255}, 17.3},
		{"light fg-4 on ink-1", [3]float64{0.640, 0.010, 255}, [3]float64{0.995, 0.002, 255}, 3.31},
		// "Lowest text contrast" of the light failed color.
		{"light failed on ink-3", [3]float64{0.530, 0.165, 38}, [3]float64{0.922, 0.007, 255}, 4.53},
	}
	for _, c := range cases {
		fg := color{oklchToLinear(c.fg[0], c.fg[1], c.fg[2]), 1}
		bg := color{oklchToLinear(c.bg[0], c.bg[1], c.bg[2]), 1}
		if got := contrastOn(fg, bg); math.Abs(got-c.want) > 0.05 {
			t.Errorf("%s: ratio %.3f, want about %.2f", c.name, got, c.want)
		}
	}
}

func TestAlphaIsBlendedOverTheGround(t *testing.T) {
	// A fully transparent color is the ground itself, so its ratio is 1;
	// a half-strength white over black sits between black and white.
	ground := color{oklchToLinear(0, 0, 0), 1}
	white := oklchToLinear(1, 0, 0)
	if got := contrastOn(color{white, 0}, ground); math.Abs(got-1) > 1e-9 {
		t.Errorf("alpha 0: ratio %v, want 1", got)
	}
	if got := contrastOn(color{white, 0.5}, ground); got <= 1 || got >= 21 {
		t.Errorf("alpha 0.5: ratio %v, want between 1 and 21", got)
	}
}

// ---- Reading the tokens of theme.css ----

// themeDark and themeLight pick a side of light-dark().
const (
	themeDark  = 0
	themeLight = 1
)

var themeNames = [2]string{"dark", "light"}

// A token declaration: a name, a colon and a value to the semicolon, all on one line.
var colorDeclaration = regexp.MustCompile(`(?m)^\s*(--color-[a-z0-9-]+):\s*([^;]+);`)

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// colorTokens returns every --color-* declaration of theme.css (the daisyUI
// plugin block and @theme) as name to raw value, in file order too.
func colorTokens(t *testing.T) (map[string]string, []string) {
	t.Helper()
	css := cssComment.ReplaceAllString(readStyle(t, "theme.css"), "")
	tokens := map[string]string{}
	var order []string
	for _, m := range colorDeclaration.FindAllStringSubmatch(css, -1) {
		if _, dup := tokens[m[1]]; dup {
			t.Errorf("%s is declared twice in theme.css", m[1])
		}
		tokens[m[1]] = strings.TrimSpace(m[2])
		order = append(order, m[1])
	}
	if len(tokens) == 0 {
		t.Fatal("no --color-* declaration found in theme.css")
	}
	return tokens, order
}

// splitArgs splits the inside of a function call at its top-level commas.
func splitArgs(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}

// call reads "name(inside)" and returns inside; ok is false for any other text.
func call(value, name string) (inside string, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, name+"(") || !strings.HasSuffix(value, ")") {
		return "", false
	}
	return value[len(name)+1 : len(value)-1], true
}

// parseOklch reads "0.76 0.12 245" or "0.76 0.12 245 / 0.55".
func parseOklch(inside string) (color, error) {
	alpha := 1.0
	if head, tail, found := strings.Cut(inside, "/"); found {
		a, err := strconv.ParseFloat(strings.TrimSpace(tail), 64)
		if err != nil {
			return color{}, fmt.Errorf("alpha in oklch(%s): %w", inside, err)
		}
		alpha, inside = a, head
	}
	fields := strings.Fields(inside)
	if len(fields) != 3 {
		return color{}, fmt.Errorf("oklch(%s) has %d numbers, want 3", inside, len(fields))
	}
	var n [3]float64
	for i, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return color{}, fmt.Errorf("oklch(%s): %w", inside, err)
		}
		n[i] = v
	}
	return color{oklchToLinear(n[0], n[1], n[2]), alpha}, nil
}

// resolve gives the color a value has in a theme: oklch(...) itself, var(--color-x)
// followed to its token, light-dark(a, b) as its dark or light side.
func resolve(tokens map[string]string, value string, theme int, depth int) (color, error) {
	if depth > 10 {
		return color{}, fmt.Errorf("var() chain too deep (a cycle?) at %q", value)
	}
	if inside, ok := call(value, "oklch"); ok {
		return parseOklch(inside)
	}
	if inside, ok := call(value, "var"); ok {
		target, found := tokens[strings.TrimSpace(inside)]
		if !found {
			return color{}, fmt.Errorf("var(%s) names no token", inside)
		}
		return resolve(tokens, target, theme, depth+1)
	}
	if inside, ok := call(value, "light-dark"); ok {
		args := splitArgs(inside)
		if len(args) != 2 {
			return color{}, fmt.Errorf("light-dark(%s) has %d arguments, want 2", inside, len(args))
		}
		return resolve(tokens, args[theme], theme, depth+1)
	}
	return color{}, fmt.Errorf("%q is neither oklch(), var() nor light-dark()", value)
}

// token resolves a token by its short name ("fg-1") in a theme.
func token(t *testing.T, tokens map[string]string, name string, theme int) color {
	t.Helper()
	raw, ok := tokens["--color-"+name]
	if !ok {
		t.Fatalf("theme.css has no --color-%s", name)
	}
	c, err := resolve(tokens, raw, theme, 0)
	if err != nil {
		t.Fatalf("--color-%s in %s: %v", name, themeNames[theme], err)
	}
	return c
}

// Every color token is one light-dark(dark, light) with a color on each side, or
// a var() to another token, so a token can't have a value for one theme only.
func TestEveryColorTokenHasBothValues(t *testing.T) {
	tokens, order := colorTokens(t)
	for _, name := range order {
		value := tokens[name]
		_, isLightDark := call(value, "light-dark")
		_, isVar := call(value, "var")
		if !isLightDark && !isVar {
			t.Errorf("%s: %q is neither light-dark(a, b) nor var(--color-x)", name, value)
			continue
		}
		for theme := range themeNames {
			if _, err := resolve(tokens, value, theme, 0); err != nil {
				t.Errorf("%s in %s: %v", name, themeNames[theme], err)
			}
		}
	}
}

// The contract's own numbers, read through the parser: a mix-up of the two
// sides of light-dark() would show here first.
func TestThemeKnownRatios(t *testing.T) {
	tokens, _ := colorTokens(t)
	cases := []struct {
		theme int
		fg    string
		want  float64
	}{
		{themeDark, "fg-1", 16.0},
		{themeLight, "fg-1", 17.3},
		{themeDark, "fg-4", 3.05},
		{themeLight, "fg-4", 3.31},
	}
	for _, c := range cases {
		got := contrastOn(token(t, tokens, c.fg, c.theme), token(t, tokens, "ink-1", c.theme))
		if math.Abs(got-c.want) > 0.05 {
			t.Errorf("%s %s on ink-1: %.2f, want about %.2f", themeNames[c.theme], c.fg, got, c.want)
		}
	}
}

// The contract's section 6: what must be readable on what, in each theme.
func TestThemeContrast(t *testing.T) {
	tokens, _ := colorTokens(t)
	grounds := []string{"ink-0", "ink-1", "ink-2", "ink-3"}
	statuses := []string{"healthy", "rolling", "warn", "failed"}

	for theme, themeName := range themeNames {
		check := func(fg, ground string, min float64) {
			t.Helper()
			got := contrastOn(token(t, tokens, fg, theme), token(t, tokens, ground, theme))
			if got < min {
				t.Errorf("--color-%s on --color-%s in %s: %.2f:1, need %.1f:1", fg, ground, themeName, got, min)
			}
		}

		// Text: the three readable foregrounds and the four status colors on every ground.
		for _, fg := range append([]string{"fg-1", "fg-2", "fg-3"}, statuses...) {
			for _, ground := range grounds {
				check(fg, ground, 4.5)
			}
		}
		// A status word in its own pill.
		for _, s := range statuses {
			check(s, s+"-bg", 4.5)
		}
		// A service's name on the panel and on the chosen rail item.
		for i := 0; i < 6; i++ {
			for _, ground := range []string{"ink-1", "ink-2"} {
				check(fmt.Sprintf("hue-%d", i), ground, 4.5)
			}
		}
		// The focus ring is a graphic, so 3:1.
		for _, ground := range []string{"ink-1", "ink-2"} {
			check("focus", ground, 3)
		}
		// fg-4 draws marks only, never text.
		check("fg-4", "ink-1", 2.5)
	}
}

// ---- No color outside the tokens ----

var (
	// A color function the stylesheet and the pages must not write.
	colorFunction = regexp.MustCompile(`\b(oklch|rgb|rgba|hsl)\(`)
	hex3          = regexp.MustCompile(`#[0-9a-fA-F]{3}\b`)
	hex6          = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
)

// The two #000 stops of the Settings nav and logs rail fades are alpha masks, not
// colors: the same in both themes, so they are the one thing allowed in the CSS.
func isMaskStop(line string, start, end int) bool {
	prefix := line[:start]
	declaration := prefix[strings.LastIndexAny(prefix, ";{")+1:]
	return line[start:end] == "#000" && strings.Contains(declaration, "mask-image")
}

// hexColorsIn lists the hex colors of a line that are not a mask stop; with
// short true it also counts #rgb, which is only safe in a stylesheet (a page
// has anchors like #add).
func hexColorsIn(line string, short bool) []string {
	var found []string
	patterns := []*regexp.Regexp{hex6}
	if short {
		patterns = append(patterns, hex3)
	}
	for _, re := range patterns {
		for _, loc := range re.FindAllStringIndex(line, -1) {
			if isMaskStop(line, loc[0], loc[1]) {
				continue
			}
			found = append(found, line[loc[0]:loc[1]])
		}
	}
	return found
}

func TestNoColorOutsideTokens(t *testing.T) {
	// The stylesheet source: tokens only. (Raw colors in comments count too.)
	inputCSS := readStyle(t, "input.css")
	for i, line := range strings.Split(inputCSS, "\n") {
		if m := colorFunction.FindString(line); m != "" {
			t.Errorf("style/input.css:%d writes %s...): use a token from theme.css", i+1, m)
		}
		for _, hex := range hexColorsIn(line, true) {
			t.Errorf("style/input.css:%d writes %s: use a token from theme.css", i+1, hex)
		}
	}

	// theme.css is where colors live: a raw color is allowed on a --color-* line only.
	themeCSS := cssComment.ReplaceAllString(readStyle(t, "theme.css"), "")
	for i, line := range strings.Split(themeCSS, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--color-") {
			continue
		}
		if m := colorFunction.FindString(line); m != "" {
			t.Errorf("style/theme.css (comments removed) line %d writes %s...) outside a --color-* token: %s", i+1, m, strings.TrimSpace(line))
		}
		for _, hex := range hexColorsIn(line, true) {
			t.Errorf("style/theme.css (comments removed) line %d writes %s outside a --color-* token", i+1, hex)
		}
	}

	// The pages and our scripts: no color functions, no #rrggbb.
	files, err := filepath.Glob(filepath.Join("templates", "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("templates/*.html: %v, %d files", err, len(files))
	}
	// terminal.js is left out on purpose: xterm.js takes hex, not CSS variables, and
	// the terminal stays dark in both themes (contract section 4). It is the one
	// permitted exception to the Token-Only Color Rule, and a comment there says so.
	// The vendored files (htmx, xterm, addon-fit) are not ours to check.
	for _, script := range []string{"console.js", "localtime.js", "logs.js", "monitoring.js", "ops.js"} {
		files = append(files, filepath.Join("static", "js", script))
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if m := colorFunction.FindString(line); m != "" {
				t.Errorf("%s:%d writes %s...): use a token", file, i+1, m)
			}
			for _, hex := range hexColorsIn(line, false) {
				t.Errorf("%s:%d writes %s: use a token", file, i+1, hex)
			}
		}
	}
}

// The guard itself: a mask stop passes, a stray color doesn't.
func TestHexColorsInFindsStrayColors(t *testing.T) {
	cases := []struct {
		line  string
		short bool
		want  int
	}{
		{`.cv-snav { mask-image: linear-gradient(to right, #000 calc(100% - 40px), transparent); }`, true, 0},
		{`.a { -webkit-mask-image: linear-gradient(#000, transparent); mask-image: linear-gradient(#000, transparent); }`, true, 0},
		{`.a { color: #000; }`, true, 1},
		{`.a { mask-image: linear-gradient(#000, transparent); color: #fff; }`, true, 1},
		{`.a { background: #0d0e11; }`, true, 1},
		{`<a href="#add">`, false, 0},
		{`<a style="color:#0d0e11">`, false, 1},
	}
	for _, c := range cases {
		if got := len(hexColorsIn(c.line, c.short)); got != c.want {
			t.Errorf("hexColorsIn(%q, %v) found %d, want %d", c.line, c.short, got, c.want)
		}
	}
}
