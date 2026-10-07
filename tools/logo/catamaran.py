#!/usr/bin/env python3
"""Generates the catamaran in assets/logo.svg from a small 3D model.

The viewer is low in the trough just in front of a breaking wave, looking
up. The catamaran charges down the face with full sails and is about to
pass close by on the viewer's right. Hulls, trampoline, mast and billowing
sails are meshed, projected through a perspective camera, Lambert-shaded and
painter-sorted into SVG polygons. Prints the <g> to stdout.

    python3 tools/logo/catamaran.py > /tmp/boat.svgfrag
"""
import math
import os

def env(name, default):
    return float(os.environ.get(name, default))

def v_add(a, b): return (a[0]+b[0], a[1]+b[1], a[2]+b[2])
def v_sub(a, b): return (a[0]-b[0], a[1]-b[1], a[2]-b[2])
def v_mul(a, k): return (a[0]*k, a[1]*k, a[2]*k)
def dot(a, b): return a[0]*b[0]+a[1]*b[1]+a[2]*b[2]
def cross(a, b): return (a[1]*b[2]-a[2]*b[1], a[2]*b[0]-a[0]*b[2], a[0]*b[1]-a[1]*b[0])
def norm(a):
    l = math.sqrt(dot(a, a)) or 1
    return v_mul(a, 1/l)

# ---- boat frame (world: x right, y up, z away from the camera) ----
heading = norm((env('HX', 0.14), -0.24, -0.96))         # at us, drifting right: it passes on our right
up = norm(v_sub((0.0, 1.0, 0.0), v_mul(heading, heading[1])))
right = norm(cross(up, heading))              # the boat's starboard (toward us)
# heel: the rig leans to leeward, away from us
heel = math.radians(12)
up, right = (norm(v_add(v_mul(up, math.cos(heel)), v_mul(right, -math.sin(heel)))),
             norm(v_add(v_mul(right, math.cos(heel)), v_mul(up, math.sin(heel)))))
origin = (env('OX', 1.6), env('OY', 3.0), env('OZ', 13))                     # boat centre in front of us, to the right

def P(fwd, side, h):
    """Boat-local (forward, starboard, up) -> world."""
    return v_add(origin, v_add(v_mul(heading, fwd), v_add(v_mul(right, side), v_mul(up, h))))

# ---- camera: at the origin, pitched up to look at the boat ----
pitch = math.radians(env('PITCH', 30))
yaw = math.radians(4)
# The projection is normalized and then fitted into BOX (x0, y0, x1, y1) in
# icon pixels, keeping proportions.
BOX = tuple(float(v) for v in os.environ.get('BOX', '86 10 246 214').split())

def cam(p):
    x, y, z = p
    x, z = x*math.cos(yaw) - z*math.sin(yaw), x*math.sin(yaw) + z*math.cos(yaw)
    y, z = y*math.cos(pitch) - z*math.sin(pitch), y*math.sin(pitch) + z*math.cos(pitch)
    return (x, y, z)

def proj(p):
    x, y, z = cam(p)
    return (x/z, -y/z, z)

LIGHT = norm((-0.45, 0.75, -0.5))  # sun over our left shoulder lights the side facing us

polys = []  # (depth, svg)

def shade(base, n, ambient=0.6):
    k = ambient + (1-ambient)*max(0.0, dot(n, LIGHT))
    return '#%02x%02x%02x' % tuple(min(255, int(c*k)) for c in base)

def face(pts, base, two_sided=True, extra='', ambient=0.6):
    n = norm(cross(v_sub(pts[1], pts[0]), v_sub(pts[2], pts[0])))
    if two_sided and dot(n, v_sub((0, 0, 0), pts[0])) < 0:
        n = v_mul(n, -1)  # face the camera
    pp = [proj(p) for p in pts]
    depth = sum(p[2] for p in pp)/len(pp)
    polys.append((depth, [(p[0], p[1]) for p in pp], shade(base, n, ambient), extra))

# ---- hulls ----
L = 9.0
def hull(side):
    secs = []
    n = 10
    for i in range(n+1):
        t = i/n                       # 0 stern .. 1 bow
        fwd = -L/2 + t*L
        w = 0.34*math.sin(math.pi*min(1, t*1.15+0.08))**0.7   # fine bow, fuller aft
        if t > 0.97: w = 0.02
        depth = 0.5*math.sin(math.pi*min(1, t*0.9+0.12))**0.5 + 0.08
        rise = 0.35*t**3                                       # bow sheer lifts
        secs.append((fwd, w, depth, rise))
    for a, b in zip(secs, secs[1:]):
        for s_out in (1, -1):
            top_a = P(a[0], side + s_out*a[1], 0.4 + a[3])
            top_b = P(b[0], side + s_out*b[1], 0.4 + b[3])
            bot_a = P(a[0], side, -a[2] + a[3]*0.5)
            bot_b = P(b[0], side, -b[2] + b[3]*0.5)
            face([top_a, top_b, bot_b, bot_a], (240, 246, 252))
        # deck
        face([P(a[0], side - a[1], 0.4 + a[3]), P(b[0], side - b[1], 0.4 + b[3]),
              P(b[0], side + b[1], 0.4 + b[3]), P(a[0], side + a[1], 0.4 + a[3])], (230, 236, 244))
    # teal boot stripe along each hull's outer side
    for a, b in zip(secs[1:-1], secs[2:]):
        for s_out in (1, -1):
            pts = [P(a[0], side + s_out*a[1]*0.92, 0.2 + a[3]), P(b[0], side + s_out*b[1]*0.92, 0.2 + b[3]),
                   P(b[0], side + s_out*b[1]*0.97, 0.36 + b[3]), P(a[0], side + s_out*a[1]*0.97, 0.36 + a[3])]
            face(pts, (20, 184, 166), ambient=0.75)

BEAM = 2.6
hull(+BEAM)
hull(-BEAM)

# ---- trampoline and beams ----
face([P(1.6, -BEAM, 0.62), P(1.6, BEAM, 0.62), P(-2.2, BEAM, 0.62), P(-2.2, -BEAM, 0.62)], (24, 40, 64), ambient=0.9)
for f in (1.8, -2.3):
    face([P(f, -BEAM, 0.6), P(f, BEAM, 0.6), P(f-0.25, BEAM, 0.85), P(f-0.25, -BEAM, 0.85)], (226, 232, 240))

# ---- mast ----
MAST_F, MAST_H = 0.6, 13.0
def mast():
    w = 0.09
    face([P(MAST_F, -w, 0.7), P(MAST_F, w, 0.7), P(MAST_F, w*0.6, MAST_H), P(MAST_F, -w*0.6, MAST_H)], (226, 232, 240), ambient=0.8)
mast()

# ---- sails as shaded meshes ----
def sail(corner_luff_bot, corner_head, corner_clew, bulge_dir, bulge, base, rows=7, cols=6, ambient=0.55):
    # Triangular patch: luff from (luff_bot -> head), clew opposite. Bulge
    # follows sin() across the chord and fades toward the head.
    def pt(u, v):
        # u along luff (0 bottom .. 1 head), v across chord (0 luff .. 1 leech)
        luff = v_add(corner_luff_bot, v_mul(v_sub(corner_head, corner_luff_bot), u))
        leech = v_add(corner_clew, v_mul(v_sub(corner_head, corner_clew), u))
        p = v_add(luff, v_mul(v_sub(leech, luff), v))
        b = bulge * math.sin(math.pi*v) * (1 - u**1.6)
        return v_add(p, v_mul(bulge_dir, b))
    grid = [[pt(i/rows, j/cols) for j in range(cols+1)] for i in range(rows+1)]
    for i in range(rows):
        for j in range(cols):
            a, b, c, d = grid[i][j], grid[i][j+1], grid[i+1][j+1], grid[i+1][j]
            face([a, b, c], base, ambient=ambient)
            face([a, c, d], base, ambient=ambient)

# Running down the wave, dead downwind, wing-on-wing: the main is eased right
# out to port and the headsail poled out to starboard, both bellying forward,
# so both face us full even though the boat is coming straight at us.
fwd = heading
BOOM = 5.0
ang = math.radians(env('BOOMANG', 72))
main_tack = P(MAST_F, 0, 1.4)
main_head = P(MAST_F, 0, MAST_H - 0.2)
main_clew = P(MAST_F - BOOM*math.cos(ang), -BOOM*math.sin(ang), 1.7)
sail(main_tack, main_head, main_clew, fwd, 1.3, (255, 255, 255), ambient=0.82)

genn_tack = P(L/2 + 1.1, 0.2, 1.0)
genn_head = P(MAST_F + 0.2, 0, MAST_H - 0.7)
genn_clew = P(MAST_F + 0.9, 3.4, 2.6)
sail(genn_tack, genn_head, genn_clew, fwd, 1.2, (45, 212, 191), ambient=0.62)

polys.sort(key=lambda p: -p[0])   # far first
xs = [x for p in polys for x, _ in p[1]]
ys = [y for p in polys for _, y in p[1]]
k = min((BOX[2]-BOX[0])/(max(xs)-min(xs)), (BOX[3]-BOX[1])/(max(ys)-min(ys)))
ox = BOX[2] - k*max(xs)            # align right
oy = BOX[1] - k*min(ys)            # align top
print('<g>')
for _, pts, fill, extra in polys:
    d = 'M' + ' L'.join('%.1f %.1f' % (ox + k*x, oy + k*y) for x, y in pts) + 'Z'
    # a hairline in the fill colour hides seams between mesh faces
    print('  <path d="%s" fill="%s" stroke="%s" stroke-width="0.35"%s/>' % (d, fill, fill, extra))
print('</g>')
