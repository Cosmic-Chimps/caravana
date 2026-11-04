import math, sys
def oklch_to_srgb_lin(L, C, h):
    a = C*math.cos(math.radians(h)); b = C*math.sin(math.radians(h))
    l_ = L + 0.3963377774*a + 0.2158037573*b
    m_ = L - 0.1055613458*a - 0.0638541728*b
    s_ = L - 0.0894841775*a - 1.2914855480*b
    l, m, s = l_**3, m_**3, s_**3
    r = 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
    g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
    bb = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
    return r, g, bb
def gamut_ok(c):
    return all(-0.0005 <= x <= 1.0005 for x in oklch_to_srgb_lin(*c))
def lum(c):
    r, g, b = [min(1, max(0, x)) for x in oklch_to_srgb_lin(*c)]
    return 0.2126*r + 0.7152*g + 0.0722*b
def blend(fg, alpha, bg):
    # blend in linear sRGB, approximate: return luminance only
    f = [min(1,max(0,x)) for x in oklch_to_srgb_lin(*fg)]; g = [min(1,max(0,x)) for x in oklch_to_srgb_lin(*bg)]
    mix = [alpha*a + (1-alpha)*b for a, b in zip(f, g)]
    return 0.2126*mix[0] + 0.7152*mix[1] + 0.0722*mix[2]
def cr(a, b):
    la = a if isinstance(a, float) else lum(a); lb = b if isinstance(b, float) else lum(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)
