"""Renders the Windstream logo with Blender (Cycles).

A catamaran bursts up and over the crest of a steep wave, bows pitched high,
seen from the trough just in front of it. It is about to pass close on the
viewer's right.

    blender -b -P tools/logo/render.py -- OUT.png SIZE SAMPLES

Everything is built from code: hulls, rig and sails are procedural meshes,
the sea is a displaced heightfield with foam and spray.
"""
import math
import random
import sys

import bmesh
import bpy
from mathutils import Euler, Matrix, Vector

import os
# Sun azimuth: 0 = straight ahead of the camera (+Y), positive = to the right.
SUN_AZ = float(os.environ.get("SUN_AZ", -14))   # in frame, behind and left of the rig
SUN_ELEV = float(os.environ.get("SUN_EL", 30))
argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
OUT = argv[0] if len(argv) > 0 else "/tmp/logo.png"
SIZE = int(argv[1]) if len(argv) > 1 else 512
SAMPLES = int(argv[2]) if len(argv) > 2 else 48

rng = random.Random(7)
bpy.ops.wm.read_factory_settings(use_empty=True)
scene = bpy.context.scene


# ---------------------------------------------------------------- helpers
def mesh_object(name, verts, faces, smooth=True, mat=None):
    me = bpy.data.meshes.new(name)
    me.from_pydata(verts, [], faces)
    me.update()
    ob = bpy.data.objects.new(name, me)
    scene.collection.objects.link(ob)
    if smooth:
        for p in me.polygons:
            p.use_smooth = True
    if mat:
        me.materials.append(mat)
    return ob


def principled(name, color, rough=0.4, metal=0.0, **extra):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    b = m.node_tree.nodes["Principled BSDF"]
    b.inputs["Base Color"].default_value = (*color, 1)
    b.inputs["Roughness"].default_value = rough
    b.inputs["Metallic"].default_value = metal
    for k, v in extra.items():
        b.inputs[k].default_value = v
    return m


def subsurf(ob, levels=2):
    m = ob.modifiers.new("smooth", "SUBSURF")
    m.levels = levels
    m.render_levels = levels


# ---------------------------------------------------------------- materials
gelcoat = principled("gelcoat", (0.92, 0.94, 0.96), rough=0.18, **{"Coat Weight": 0.6, "Coat Roughness": 0.05})
stripe = principled("stripe", (0.02, 0.55, 0.50), rough=0.25, **{"Coat Weight": 0.5})
antifoul = principled("antifoul", (0.03, 0.06, 0.12), rough=0.5)
tramp = principled("trampoline", (0.03, 0.04, 0.06), rough=0.8, **{"Alpha": 0.55})
alu = principled("aluminium", (0.55, 0.58, 0.62), rough=0.3, metal=1.0)
carbon = principled("carbon", (0.02, 0.02, 0.025), rough=0.25, **{"Coat Weight": 0.8})
rope = principled("rope", (0.75, 0.75, 0.72), rough=0.6)


def sail_material(name, color, translucency):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    nt = m.node_tree
    nt.nodes.clear()
    out = nt.nodes.new("ShaderNodeOutputMaterial")
    bsdf = nt.nodes.new("ShaderNodeBsdfPrincipled")
    bsdf.inputs["Base Color"].default_value = (*color, 1)
    bsdf.inputs["Roughness"].default_value = 0.45
    trans = nt.nodes.new("ShaderNodeBsdfTranslucent")
    trans.inputs["Color"].default_value = (*color, 1)
    mix = nt.nodes.new("ShaderNodeMixShader")
    mix.inputs["Fac"].default_value = translucency
    nt.links.new(bsdf.outputs[0], mix.inputs[1])
    nt.links.new(trans.outputs[0], mix.inputs[2])
    nt.links.new(mix.outputs[0], out.inputs["Surface"])
    return m


mainsail_mat = sail_material("mainsail", (0.95, 0.95, 0.93), 0.35)
jib_mat = sail_material("jib", (0.05, 0.75, 0.68), 0.4)

# ---------------------------------------------------------------- the boat
# Built in boat space: +X forward (bow), +Y port, +Z up; origin at the
# centre of the trampoline. Then posed as one parent.
boat = bpy.data.objects.new("boat", None)
scene.collection.objects.link(boat)
L, BEAM = 9.0, float(os.environ.get("BEAM", 2.1))   # beam ~47% of length, like a real racing cat
BOAT_X = float(os.environ.get("BOAT_X", 3.5))
CREST_Y, H = 14.0, 6.2  # hull length, hull centre offset from the middle


def hull(y0):
    """A slender racing-cat hull: full U sections aft, a fine entry, the keel
    line sweeping up into a slightly raked stem."""
    secs, ring = 40, 16
    verts, faces = [], []
    for i in range(secs + 1):
        t = i / secs                      # 0 stern .. 1 bow
        x = -L / 2 + t * L
        fine = max(0.0, (t - 0.5) / 0.5)
        w = 0.38 * (1 - fine ** 1.7) ** 0.75
        if t < 0.04:
            w *= 0.93 + 0.07 * t / 0.04   # slight tuck at the transom
        w = max(w, 0.012)
        d = 0.72 * (1 - fine ** 2.2) + 0.04   # keel sweeps up into the bow
        top = 0.62 + 0.22 * t ** 3            # sheer rises a little forward
        for j in range(ring):
            a = math.pi * j / (ring - 1)      # starboard deck edge .. port deck edge
            s_ = math.sin(a)
            yy = -math.cos(a) * w
            zz = -(s_ ** 0.55) * d            # full, rounded U section
            if j in (0, ring - 1):
                zz = top
            elif j in (1, ring - 2):
                zz = top * 0.55
            # raked stem: the lower part of the bow sits further aft
            xx = x - 0.55 * (fine ** 3) * max(0.0, -zz) / max(d, 0.05)
            verts.append((xx, y0 + yy, zz))
    for i in range(secs):
        for j in range(ring - 1):
            a = i * ring + j
            faces.append((a, a + 1, a + ring + 1, a + ring))
        faces.append((i * ring + ring - 1, i * ring, (i + 1) * ring, (i + 1) * ring + ring - 1))  # deck
    faces.append(tuple(range(ring - 1, -1, -1)))  # transom
    ob = mesh_object("hull", verts, faces, mat=gelcoat)
    ob.data.materials.append(stripe)
    ob.data.materials.append(antifoul)
    for p in ob.data.polygons:
        zc = p.center.z
        if zc < -0.3:
            p.material_index = 2
        elif 0.12 < zc < 0.24:
            p.material_index = 1
    subsurf(ob, 2)
    ob.parent = boat
    return ob


hull(BEAM)
hull(-BEAM)



def box(name, center, size, mat, rot=(0, 0, 0)):
    bm = bmesh.new()
    bmesh.ops.create_cube(bm, size=1.0)
    me = bpy.data.meshes.new(name)
    bm.to_mesh(me)
    ob = bpy.data.objects.new(name, me)
    scene.collection.objects.link(ob)
    ob.scale = size
    ob.location = center
    ob.rotation_euler = rot
    me.materials.append(mat)
    ob.parent = boat
    return ob


def rod(name, a, b, r, mat):
    a, b = Vector(a), Vector(b)
    bm = bmesh.new()
    bmesh.ops.create_cone(bm, cap_ends=True, segments=12, radius1=r, radius2=r * 0.8, depth=(b - a).length)
    me = bpy.data.meshes.new(name)
    bm.to_mesh(me)
    ob = bpy.data.objects.new(name, me)
    scene.collection.objects.link(ob)
    ob.location = (a + b) / 2
    ob.rotation_euler = (b - a).to_track_quat("Z", "Y").to_euler()
    me.materials.append(mat)
    for p in me.polygons:
        p.use_smooth = True
    ob.parent = boat
    return ob


# Daggerboards and rudders: dark carbon foils knifing down from each hull.
foil = principled("foil", (0.03, 0.03, 0.035), rough=0.3, **{"Coat Weight": 0.6})
BOARD_X, BOARD_DEPTH = float(os.environ.get("BOARD_X", -0.5)), 1.5   # just aft of the mast
for side in (BEAM, -BEAM):
    box("daggerboard", (BOARD_X, side, -0.45 - BOARD_DEPTH / 2), (0.38, 0.045, BOARD_DEPTH), foil)
    box("rudder", (-L / 2 + 0.25, side, -0.55 - 0.45), (0.3, 0.04, 0.9), foil)

# trampoline and beams
mesh_object("trampoline", [(1.6, -BEAM, 0.5), (1.6, BEAM, 0.5), (-2.6, BEAM, 0.5), (-2.6, -BEAM, 0.5)],
            [(0, 1, 2, 3)], mat=tramp).parent = boat
rod("front beam", (1.7, -BEAM, 0.55), (1.7, BEAM, 0.55), 0.09, alu)
rod("rear beam", (-2.7, -BEAM, 0.55), (-2.7, BEAM, 0.55), 0.09, alu)
rod("dolphin striker", (4.6, 0, 0.45), (1.7, 0, 0.5), 0.04, alu)

MAST_X, MAST_H = 0.9, 14.0
rod("mast", (MAST_X, 0, 0.55), (MAST_X, 0, MAST_H), 0.11, carbon)
for side in (BEAM, -BEAM):
    rod("shroud", (MAST_X - 0.3, side, 0.7), (MAST_X, 0, MAST_H * 0.86), 0.012, rope)
rod("forestay", (4.6, 0, 0.6), (MAST_X, 0, MAST_H * 0.92), 0.012, rope)


def sail(name, luff_bot, head, clew, lee, depth, twist, mat, rows=30, cols=20):
    """A sail filled by the wind.

    Each horizontal section runs from the luff (mast/forestay) to the leech.
    The wind pushes the cloth out to leeward into a curved aerofoil: camber
    `depth` (fraction of the chord) with its deepest point ~40% aft, at right
    angles to that section's chord. Sections twist further to leeward with
    height (`twist`, radians at the head), as real sails do because the wind
    is stronger aloft and the leech is not infinitely tight.
    """
    luff_bot, head, clew, lee = Vector(luff_bot), Vector(head), Vector(clew), Vector(lee).normalized()
    axis = (head - luff_bot).normalized()
    verts, faces = [], []
    for i in range(rows + 1):
        u = i / rows
        luff = luff_bot.lerp(head, u)
        leech = clew.lerp(head, u)
        chord = leech - luff
        # twist the section about the luff, toward leeward
        rot = Matrix.Rotation(twist * u, 3, axis)
        if (rot @ chord).dot(lee) < chord.dot(lee):
            rot = Matrix.Rotation(-twist * u, 3, axis)
        chord = rot @ chord
        normal = axis.cross(chord).normalized()
        if normal.dot(lee) < 0:
            normal = -normal
        clen = chord.length
        for j in range(cols + 1):
            v = j / cols
            shape = math.sin(math.pi * v ** 0.72)          # deepest ~40% aft
            p = luff + chord * v + normal * (depth * clen * shape * (1 - 0.25 * u))
            verts.append(p[:])
    for i in range(rows):
        for j in range(cols):
            a = i * (cols + 1) + j
            faces.append((a, a + 1, a + cols + 2, a + cols + 1))
    ob = mesh_object(name, verts, faces, mat=mat)
    sol = ob.modifiers.new("cloth thickness", "SOLIDIFY")
    sol.thickness = 0.01
    ob.parent = boat
    return ob


# Running before the wind toward us: the wind blows from astern, so the
# mainsail is eased right out, perpendicular to the hulls, and the jib is
# goosewinged out the other side. Both fill forward (+X), bellying toward us.
FWD = (1.0, 0.0, 0.0)
BOOM = 4.6
boom_ang = math.radians(float(os.environ.get("BOOM", 84)))
# main out to starboard (toward the viewer), jib goosewinged to port
clew = (MAST_X - BOOM * math.cos(boom_ang), -BOOM * math.sin(boom_ang), 1.6)
sail("mainsail", (MAST_X - 0.1, 0, 1.2), (MAST_X - 0.1, 0, MAST_H - 0.2), clew, FWD, 0.14, math.radians(12), mainsail_mat)
rod("boom", (MAST_X, 0, 1.4), (clew[0], clew[1], clew[2] - 0.05), 0.06, carbon)
jib_ang = math.radians(78)
jib_clew = (MAST_X + 1.0 - 3.6 * math.cos(jib_ang), 3.6 * math.sin(jib_ang), 1.4)
sail("jib", (4.5, 0, 0.75), (MAST_X + 0.15, 0, MAST_H * 0.9), jib_clew, FWD, 0.16, math.radians(8), jib_mat)
rod("whisker pole", (MAST_X + 0.2, 0, 1.3), jib_clew, 0.04, carbon)

# Pose: on the crest, bows pitched high, heeled with the windward (near)
# hull lifting, heading at us and slightly right so it passes on our right.
boat.rotation_mode = "XYZ"
heading = math.radians(-90 + float(os.environ.get("YAW", 8)))   # +X(bow) rotated to face the camera (-Y), angled right
PITCH = float(os.environ.get("PITCH", 3))   # bows up, degrees
HEEL = float(os.environ.get("HEEL", 3))
boat.rotation_euler = Euler((math.radians(-HEEL), math.radians(-PITCH), heading), "XYZ")
# Hulls skimming the crest with the foils cut into the wave.
# Cresting: the boards and the after half of the hulls are in the crest,
# the bows overhang the face toward us.
boat.location = (BOAT_X, CREST_Y + float(os.environ.get("BACK", 1.6)), H + float(os.environ.get("LIFT", 0.55)))

# ---------------------------------------------------------------- the sea
CREST_Y, H = 14.0, 6.2


CHOP = [(rng.uniform(0.015, 0.05), rng.uniform(0.5, 2.6), rng.uniform(0, 2 * math.pi), rng.uniform(0, 6.3))
        for _ in range(28)]


def height(x, y):
    # A long swell ridge across the view: steep face toward us, gentle back.
    dy = y - CREST_Y
    w = 4.2 if dy < 0 else 12.0
    h = H * math.exp(-(dy / w) ** 2)
    # the ridge sags a little to the left and right
    h *= 1.0 - 0.10 * (x / 40.0) ** 2
    # wind chop: a handful of short waves from scattered directions
    for amp, k, ang, ph in CHOP:
        h += amp * math.sin(k * (x * math.cos(ang) + y * math.sin(ang)) + ph)
    return h


BOAT_X = float(os.environ.get("BOAT_X", 3.5))
nx, ny = 240, 220
x0, x1, y0, y1 = -40.0, 40.0, -4.0, 80.0
verts, faces, foam = [], [], []
for j in range(ny + 1):
    # denser rows near the camera and the crest
    v = j / ny
    y = y0 + (y1 - y0) * (v ** 1.6)
    for i in range(nx + 1):
        x = x0 + (x1 - x0) * i / nx
        z = height(x, y)
        verts.append((x, y, z))
        crest = max(0.0, (z - 0.8 * H) / (0.2 * H))
        # churned water where the hulls and boards cut through the crest
        near_boat = sum(math.exp(-((x - hx) ** 2 / 0.8 + (y - CREST_Y) ** 2 / 3.0)) for hx in (BOAT_X - BEAM, BOAT_X + BEAM))
        f = min(1.0, 0.8 * crest + 0.75 * near_boat)
        foam.append(max(0.0, f))
for j in range(ny):
    for i in range(nx):
        a = j * (nx + 1) + i
        faces.append((a, a + 1, a + nx + 2, a + nx + 1))
sea = mesh_object("sea", verts, faces)
attr = sea.data.color_attributes.new("foam", "FLOAT_COLOR", "POINT")
for k, f in enumerate(foam):
    attr.data[k].color = (f, f, f, 1.0)

water = bpy.data.materials.new("water")
water.use_nodes = True
nt = water.node_tree
nt.nodes.clear()
out = nt.nodes.new("ShaderNodeOutputMaterial")
wbsdf = nt.nodes.new("ShaderNodeBsdfPrincipled")
wbsdf.inputs["Base Color"].default_value = (0.0, 0.10, 0.12, 1)
wbsdf.inputs["Roughness"].default_value = 0.04
wbsdf.inputs["IOR"].default_value = 1.33
wbsdf.inputs["Subsurface Weight"].default_value = 0.0
# light glowing through thin water near the crest: a little emission-free
# teal in the base colour, driven by height
geo = nt.nodes.new("ShaderNodeNewGeometry")
sep = nt.nodes.new("ShaderNodeSeparateXYZ")
nt.links.new(geo.outputs["Position"], sep.inputs[0])
ramp = nt.nodes.new("ShaderNodeValToRGB")
ramp.color_ramp.elements[0].position = 0.0
ramp.color_ramp.elements[0].color = (0.0, 0.018, 0.035, 1)
ramp.color_ramp.elements[1].position = 1.0
ramp.color_ramp.elements[1].color = (0.0, 0.30, 0.27, 1)
div = nt.nodes.new("ShaderNodeMath")
div.operation = "DIVIDE"
div.inputs[1].default_value = H
nt.links.new(sep.outputs["Z"], div.inputs[0])
nt.links.new(div.outputs[0], ramp.inputs[0])
nt.links.new(ramp.outputs[0], wbsdf.inputs["Base Color"])
# fine ripples
noise = nt.nodes.new("ShaderNodeTexNoise")
noise.inputs["Scale"].default_value = 6.0
noise.inputs["Detail"].default_value = 8
bump = nt.nodes.new("ShaderNodeBump")
bump.inputs["Strength"].default_value = 0.06
nt.links.new(noise.outputs["Fac"], bump.inputs["Height"])
nt.links.new(bump.outputs["Normal"], wbsdf.inputs["Normal"])
# foam: white, rough, broken up by noise
foam_bsdf = nt.nodes.new("ShaderNodeBsdfPrincipled")
foam_bsdf.inputs["Base Color"].default_value = (0.92, 0.96, 0.97, 1)
foam_bsdf.inputs["Roughness"].default_value = 0.7
attr_node = nt.nodes.new("ShaderNodeVertexColor")
attr_node.layer_name = "foam"
fnoise = nt.nodes.new("ShaderNodeTexNoise")
fnoise.inputs["Scale"].default_value = 1.6
fnoise.inputs["Detail"].default_value = 12
fnoise.inputs["Roughness"].default_value = 0.65
mul = nt.nodes.new("ShaderNodeMath")
mul.operation = "MULTIPLY"
nt.links.new(attr_node.outputs["Color"], mul.inputs[0])
nt.links.new(fnoise.outputs["Fac"], mul.inputs[1])
fr = nt.nodes.new("ShaderNodeValToRGB")
fr.color_ramp.elements[0].position = 0.28
fr.color_ramp.elements[1].position = 0.42
nt.links.new(mul.outputs[0], fr.inputs[0])
mixw = nt.nodes.new("ShaderNodeMixShader")
nt.links.new(fr.outputs[0], mixw.inputs["Fac"])
nt.links.new(wbsdf.outputs[0], mixw.inputs[1])
nt.links.new(foam_bsdf.outputs[0], mixw.inputs[2])
nt.links.new(mixw.outputs[0], out.inputs["Surface"])
sea.data.materials.append(water)

# ---------------------------------------------------------------- spray
spray_mat = principled("spray", (0.95, 0.98, 1.0), rough=0.3, **{"Transmission Weight": 0.6, "IOR": 1.33})
bm = bmesh.new()
boat_mx = boat.matrix_world.copy()
bpy.context.view_layer.update()
boat_mx = boat.matrix_world.copy()


def drop(center, r):
    m = Matrix.Translation(center) @ Matrix.Scale(r, 4)
    bmesh.ops.create_icosphere(bm, subdivisions=1, radius=1.0, matrix=m)


# Spray where the bows and foils slice into the wave: a fan of droplets
# peeling up and back from each cut, plus a lower sheet off each bow.
R = boat_mx.to_3x3()
fwd_w = (R @ Vector((1, 0, 0))).normalized()
up_w = Vector((0, 0, 1))
for side in (BEAM, -BEAM):
    out_w = (R @ Vector((0, 1 if side > 0 else -1, 0))).normalized()
    for cut_x, count, spread in ((BOARD_X, 900, 1.0), (L / 2 - 0.9, 700, 0.7), (-L / 2 + 0.25, 300, 0.6)):
        p0 = boat_mx @ Vector((cut_x, side, -0.75))
        for _ in range(count):
            a = rng.random() ** 0.8 * 2.8 * spread          # distance back along the hull
            rise = (0.25 + 1.6 * a ** 0.6) * rng.random() ** 0.5
            lateral = rng.gauss(0, 0.25 + 0.35 * a)
            p = p0 - fwd_w * a + up_w * rise + out_w * abs(lateral) * (1 if rng.random() < 0.8 else -1)
            drop(p, abs(rng.gauss(0.011, 0.006)) + 0.003)
for _ in range(1100):
    x = BOAT_X + rng.gauss(0, 7)
    y = CREST_Y + abs(rng.gauss(0.3, 1.2))   # behind the crest line
    z = height(x, y) + abs(rng.gauss(0, 0.8))
    if abs(x - BOAT_X) < 6:
        z += abs(rng.gauss(0, 1.2))
    drop(Vector((x, y, z)), abs(rng.gauss(0.014, 0.008)) + 0.004)
me = bpy.data.meshes.new("spray")
bm.to_mesh(me)
spray = bpy.data.objects.new("spray", me)
scene.collection.objects.link(spray)
me.materials.append(spray_mat)

# ---------------------------------------------------------------- camera aim
CAM_Z = float(os.environ.get("CAM_Z", 6.0))   # just below hull height
CAM_LOC = Vector((float(os.environ.get("CAM_X", 0.2)), float(os.environ.get("CAM_Y", 8.5)), CAM_Z))
CAM_TARGET = Vector((BOAT_X - 0.6, CREST_Y, float(os.environ.get("TGT_Z", 9.0))))
CAM_ROT = (CAM_TARGET - CAM_LOC).to_track_quat("-Z", "Y")
_cam_fwd = CAM_ROT @ Vector((0, 0, -1))
_cam_up = CAM_ROT @ Vector((0, 1, 0))
_cam_right = CAM_ROT @ Vector((1, 0, 0))

# ---------------------------------------------------------------- light, sky
world = bpy.data.worlds.new("sky")
scene.world = world
world.use_nodes = True
wn = world.node_tree
sky = wn.nodes.new("ShaderNodeTexSky")
sky.sky_type = "NISHITA"
# The sun sits in the top-left of the frame, behind the boat, so its shadows
# fall forward toward the viewer. SUN_X/SUN_Y place it in camera terms
# (fractions toward the left edge and the top edge).
SUN_DIR = (_cam_fwd - _cam_right * float(os.environ.get("SUN_X", 0.62))
           + _cam_up * float(os.environ.get("SUN_Y", 0.5))).normalized()
SUN_EL = math.asin(SUN_DIR.z)
SUN_ROT = math.atan2(SUN_DIR.x, SUN_DIR.y)
sky.sun_elevation = SUN_EL
sky.sun_rotation = SUN_ROT             # low, behind the boat
sky.altitude = 10
sky.air_density = 1.0
sky.dust_density = 0.25
sky.ozone_density = 2.0
sky.sun_disc = True
bg = wn.nodes["Background"]
bg.inputs["Strength"].default_value = float(os.environ.get("SKY", 0.6))
wn.links.new(sky.outputs[0], bg.inputs["Color"])

sun = bpy.data.lights.new("sun", "SUN")
sun.energy = float(os.environ.get("SUN_E", 6.5))
sun.angle = math.radians(0.6)          # small disc: crisp shadows
sun.color = (1.0, 0.84, 0.62)          # warm summer sun
sun_ob = bpy.data.objects.new("sun", sun)
scene.collection.objects.link(sun_ob)
sun_ob.rotation_euler = SUN_DIR.to_track_quat("Z", "Y").to_euler()  # shines along -SUN_DIR

# soft front fill so the hull undersides facing us are not black
fill = bpy.data.lights.new("fill", "AREA")
fill.energy = float(os.environ.get("FILL", 3500))
fill.size = 20
fill.color = (0.75, 0.85, 1.0)
fill_ob = bpy.data.objects.new("fill", fill)
scene.collection.objects.link(fill_ob)
fill_ob.location = (-6, -14, 3)
fill_ob.rotation_euler = (Vector((BOAT_X, CREST_Y, 5)) - Vector((-6, -14, 3))).to_track_quat("-Z", "Y").to_euler()

# ---------------------------------------------------------------- camera
cam = bpy.data.cameras.new("cam")
cam.lens = float(os.environ.get("LENS", 46))
FISHEYE = float(os.environ.get("FISHEYE", 18.0))  # mm; 0 = ordinary lens
if FISHEYE > 0:
    cam.type = "PANO"
    for target_obj in (cam, getattr(cam, "cycles", None)):
        if target_obj is None:
            continue
        try:
            target_obj.panorama_type = "FISHEYE_EQUISOLID"
            target_obj.fisheye_lens = FISHEYE
            target_obj.fisheye_fov = math.radians(180)
        except (AttributeError, TypeError):
            pass
    cam.sensor_width = float(os.environ.get("SENSOR", 24))
cam_ob = bpy.data.objects.new("cam", cam)
scene.collection.objects.link(cam_ob)
cam_ob.location = CAM_LOC
cam_ob.rotation_euler = CAM_ROT.to_euler()
scene.camera = cam_ob

# ---------------------------------------------------------------- render
scene.render.engine = "CYCLES"
scene.cycles.device = "CPU"
scene.cycles.samples = SAMPLES
# No denoiser in every Blender build: render large with enough samples and
# downscale instead.
scene.cycles.use_denoising = False
scene.cycles.max_bounces = 8
scene.render.resolution_x = SIZE
scene.render.resolution_y = SIZE
scene.render.film_transparent = False
try:
    scene.view_settings.view_transform = "AgX"
    scene.view_settings.look = "AgX - High Contrast"
except Exception:
    scene.view_settings.view_transform = "Filmic"
scene.view_settings.exposure = float(os.environ.get("EXPOSURE", -1.1))
# Warm summer grade and a soft bloom around the sun.
scene.use_nodes = True
ct = scene.node_tree
ct.nodes.clear()
rl = ct.nodes.new("CompositorNodeRLayers")
glare = ct.nodes.new("CompositorNodeGlare")
glare.glare_type = "FOG_GLOW"
glare.quality = "HIGH"
glare.threshold = 3.0
glare.size = 7
glare.mix = -0.78
cb = ct.nodes.new("CompositorNodeColorBalance")
cb.correction_method = "LIFT_GAMMA_GAIN"
cb.gain = (1.07, 1.0, 0.88)
cb.lift = (1.0, 0.99, 0.97)
comp = ct.nodes.new("CompositorNodeComposite")
ct.links.new(rl.outputs["Image"], glare.inputs["Image"])
ct.links.new(glare.outputs["Image"], cb.inputs["Image"])
ct.links.new(cb.outputs["Image"], comp.inputs["Image"])
scene.render.image_settings.file_format = "PNG"
scene.render.filepath = OUT
bpy.ops.render.render(write_still=True)
print("rendered", OUT)
