"""Builds and renders the pre-rendered clips of the /gacha card-pack wish.

One clip per rarity. A foil pack floats in a dark, hazy room and turns once.
It trembles while its right seam starts to glow in the rarity colour, then is
torn down the right side from top to bottom in two tugs, the way real foil
packs open: the strip peels toward the camera and rips free, sparks spray, and
light pours out of the slit. Pack and card tip together, the pack slides off
the card toward the bottom left, and the card spins toward the camera,
landing face up with a shockwave. Its holographic face carries the rank
letter and the stars; the bot's cardpack package draws the label onto the
card and adds the flashes on top.

Besides the frames, the script writes track.json: for every frame, where the
card's label area lands on screen, how squarely the face points at the camera,
how far the shine sweep has crossed the card, and how visible the label is.
Go warps the label with it, so the label sticks to the card as it moves.

Run it through `scripts/gen-assets.sh gacha`, which renders each rarity and
encodes the frames into internal/modules/random/cardpack/assets/. On its own
(Blender 4.2 or newer, Eevee):

    blender -b --factory-startup -P scripts/blender/gacha-pack.py -- \\
        --rarity 5 --out <frames-dir> [--frames 1,60,120] [--percent 50]

Frames land in <frames-dir>/frame-0001.png and up, the track beside them.
"""

import argparse
import json
import math
import os
import random
import sys

import bpy
from bpy_extras.object_utils import world_to_camera_view
from mathutils import Euler, Matrix, Vector

FPS = 30
# Must match Width, Height, FPS and DurationSeconds in
# internal/modules/random/cardpack/cardpack.go.
WIDTH, HEIGHT = 540, 960
TOTAL_SECONDS = 7.0

# Seconds from the start of the clip. The bot's flashes use TEAR and LAND too
# (cardpack.go); change them together.
# The closed pack turns once, trembles, and is torn down its right side in
# two tugs; the strip snaps off at TEAR. The card stays upright while the
# pack slides off it to the left, turning as it goes and dropping away down
# left; then the card spins 3.5 turns to land face on.
PACK_TURN = (0.2, 0.35, 1.2, 1.42)  # wind back, turn, settle, settled
TREMBLE_START = 1.35
TUGS = ((1.55, 1.74, 0.3), (1.80, 2.10, 1.0))  # start, end, fraction torn by the end
TEAR = 2.10
# The pack turns up to PACK_ROLL counter-clockwise as it slides, starting
# once the card is ROLL_FROM out, so no corner of the card pokes through the
# crimped seals.
PACK_ROLL = math.radians(50)
ROLL_FROM, ROLL_TO = 1.2, 4.5
SLIDE_START, SLIDE_TIME = 2.30, 0.70
CARD_CLEAR = 2.73
PACK_EXIT_END = 3.3
SPIN_START, LAND = 2.78, 4.0
LABEL_IN = (3.72, 4.10)
STARS_START, STAR_STEP = 4.30, 0.14
SWEEPS = ((4.85, 5.55), (6.15, 6.8))

# Card size in Blender units; its face looks toward -Y when unrotated.
# Thin as a playing card.
CARD_W, CARD_H, CARD_T = 2.4, 3.36, 0.01
CARD_RADIUS = 0.15
# The label area on the face, in card units from its centre. Go fits the
# label inside it.
LABEL_BOX = (-1.0, -0.62, 1.0, -0.06)  # left, bottom, right, top
RANK_Z = 0.62
STARS_Z = -0.98

# Pack size; it bulges toward the camera like a filled foil pouch.
PACK_W, PACK_H = 3.0, 4.5
PACK_BULGE = 0.24
SEAL = 0.3
PACK_Y = 1.0

CAMERA_START = Vector((0, -10.4, 0.15))
CAMERA_HOLD = Vector((0, -9.4, 0.05))
CARD_REST = Vector((0, -0.6, 0.12))

PALETTES = {
    3: {"glow": (0.25, 0.55, 1.0), "core": (0.85, 0.93, 1.0), "deep": (0.008, 0.02, 0.07), "tint": (0.03, 0.12, 0.42),
        "metal": (0.72, 0.8, 0.95), "rank": "B"},
    4: {"glow": (0.62, 0.3, 1.0), "core": (0.95, 0.86, 1.0), "deep": (0.03, 0.008, 0.07), "tint": (0.2, 0.05, 0.45),
        "metal": (0.85, 0.7, 1.0), "rank": "A"},
    5: {"glow": (1.0, 0.6, 0.14), "core": (1.0, 0.95, 0.8), "deep": (0.05, 0.025, 0.006), "tint": (0.42, 0.18, 0.02),
        "metal": (1.0, 0.76, 0.33), "rank": "S"},
}

# How loud each rarity is; every beat exists at every rarity.
TIERS = {
    3: {"sparks": 30, "burst": 50, "embers": 20, "beam": 900, "rays": 0.25, "rings": 1, "streaks": False, "holo": 0.55},
    4: {"sparks": 45, "burst": 90, "embers": 40, "beam": 1500, "rays": 0.6, "rings": 2, "streaks": True, "holo": 0.8},
    5: {"sparks": 60, "burst": 150, "embers": 80, "beam": 2600, "rays": 1.0, "rings": 3, "streaks": True, "holo": 1.0},
}

PREFIX = "Gacha"
SERIF = "/usr/share/fonts/truetype/dejavu/DejaVuSerif-Bold.ttf"


def frame_at(seconds):
    """Blender frame number (1-based) shown at `seconds` into the clip."""
    return 1 + round(seconds * FPS)


def seconds_at(frame):
    return (frame - 1) / FPS


LAST_FRAME = frame_at(TOTAL_SECONDS) - 1


def clamp01(value):
    return min(1.0, max(0.0, value))


def progress(seconds, start, end):
    return clamp01((seconds - start) / (end - start))


def smooth(t):
    return t * t * (3 - 2 * t)


def ease_out_cubic(t):
    return 1 - (1 - t) ** 3


def ease_out_quart(t):
    return 1 - (1 - t) ** 4


def torn_fraction(s):
    """How far down the right side the tear has run: the first tug starts
    slowly through the top seal, the second accelerates and snaps the bottom."""
    done = 0.0
    for start, end, reach in TUGS:
        p = progress(s, start, end)
        if p <= 0:
            break
        done = lerp(done, reach, smooth(p) if reach < 1 else p ** 1.6)
    return done


def lerp(a, b, t):
    return a + (b - a) * t


# --- scene plumbing -------------------------------------------------------


def reset():
    for scene in [s for s in bpy.data.scenes if s.name.startswith(PREFIX)]:
        for obj in list(scene.objects):
            bpy.data.objects.remove(obj, do_unlink=True)
        bpy.data.scenes.remove(scene)
    for collection in (bpy.data.meshes, bpy.data.materials, bpy.data.lights, bpy.data.cameras,
                       bpy.data.curves, bpy.data.worlds, bpy.data.node_groups):
        for block in [b for b in collection if b.name.startswith(PREFIX) and b.users == 0]:
            collection.remove(block)


def new_scene():
    scene = bpy.data.scenes.new(f"{PREFIX}Scene")
    scene.render.engine = "BLENDER_EEVEE"
    scene.render.resolution_x = WIDTH
    scene.render.resolution_y = HEIGHT
    scene.render.resolution_percentage = 100
    scene.render.fps = FPS
    scene.render.image_settings.file_format = "PNG"
    scene.render.image_settings.color_mode = "RGB"
    scene.render.use_motion_blur = True
    scene.render.motion_blur_shutter = 0.5
    # Centred, so the blur straddles the instant the bot's track samples.
    scene.render.motion_blur_position = "CENTER"
    scene.eevee.motion_blur_steps = 2
    scene.frame_start = 1
    scene.frame_end = LAST_FRAME
    eevee = scene.eevee
    eevee.taa_render_samples = 48
    eevee.use_raytracing = True
    eevee.volumetric_tile_size = "4"
    eevee.volumetric_samples = 48
    eevee.volumetric_start = 0.5
    eevee.volumetric_end = 30
    eevee.use_volumetric_shadows = True
    scene.view_settings.view_transform = "AgX"
    scene.view_settings.look = "AgX - Punchy"
    return scene


def link(scene, obj):
    scene.collection.objects.link(obj)
    return obj


def node_material(name):
    material = bpy.data.materials.new(f"{PREFIX}{name}")
    if material.node_tree is None:
        material.use_nodes = True
    material.node_tree.nodes.clear()
    return material, material.node_tree.nodes, material.node_tree.links


def math_node(nodes, links, operation, a=None, b=None, clamp=False):
    """Math node fed by sockets or constants."""
    node = nodes.new("ShaderNodeMath")
    node.operation = operation
    node.use_clamp = clamp
    for index, value in enumerate((a, b)):
        if value is None:
            continue
        if isinstance(value, (int, float)):
            node.inputs[index].default_value = value
        else:
            links.new(value, node.inputs[index])
    return node.outputs[0]


def map_range(nodes, links, value, from_min, from_max, to_min=0.0, to_max=1.0, smooth_step=True):
    node = nodes.new("ShaderNodeMapRange")
    node.interpolation_type = "SMOOTHSTEP" if smooth_step else "LINEAR"
    links.new(value, node.inputs["Value"])
    node.inputs["From Min"].default_value = from_min
    node.inputs["From Max"].default_value = from_max
    node.inputs["To Min"].default_value = to_min
    node.inputs["To Max"].default_value = to_max
    return node.outputs["Result"]


def mix_rgb(nodes, links, factor, a, b, blend="MIX"):
    mix = nodes.new("ShaderNodeMix")
    mix.data_type = "RGBA"
    mix.blend_type = blend
    sockets = {s.identifier: s for s in mix.inputs}
    for socket, value in ((sockets["Factor_Float"], factor), (sockets["A_Color"], a), (sockets["B_Color"], b)):
        if isinstance(value, (int, float)):
            socket.default_value = value
        elif isinstance(value, tuple):
            socket.default_value = (*value[:3], 1)
        else:
            links.new(value, socket)
    return next(o for o in mix.outputs if o.identifier == "Result_Color")


def glow_material(name, color, strength, falloff=0.0):
    """Additive emission that never occludes. With falloff, the glow fades
    toward the silhouette, so round meshes read as soft balls of light."""
    material, nodes, links = node_material(name)
    output = nodes.new("ShaderNodeOutputMaterial")
    add = nodes.new("ShaderNodeAddShader")
    transparent = nodes.new("ShaderNodeBsdfTransparent")
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = (*color, 1)
    scale = nodes.new("ShaderNodeMath")
    scale.operation = "MULTIPLY"
    scale.name = "Strength"
    scale.inputs[0].default_value = 1.0
    scale.inputs[1].default_value = strength
    if falloff > 0:
        weight = nodes.new("ShaderNodeLayerWeight")
        weight.inputs["Blend"].default_value = 0.5
        facing = math_node(nodes, links, "SUBTRACT", 1.0, weight.outputs["Facing"])
        links.new(math_node(nodes, links, "POWER", facing, falloff), scale.inputs[0])
    links.new(scale.outputs[0], emission.inputs["Strength"])
    links.new(transparent.outputs[0], add.inputs[0])
    links.new(emission.outputs[0], add.inputs[1])
    links.new(add.outputs[0], output.inputs["Surface"])
    material.surface_render_method = "BLENDED"
    return material


def strength_socket(material):
    return material.node_tree.nodes["Strength"].inputs[1]


def metal_material(name, color, roughness=0.22, emission=None, emission_strength=0.0):
    material, nodes, links = node_material(name)
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    bsdf.inputs["Base Color"].default_value = (*color, 1)
    bsdf.inputs["Metallic"].default_value = 1.0
    bsdf.inputs["Roughness"].default_value = roughness
    if emission:
        bsdf.inputs["Emission Color"].default_value = (*emission, 1)
        bsdf.inputs["Emission Strength"].default_value = emission_strength
    # Fine brushed grain, so highlights break up like real metal leaf.
    noise = nodes.new("ShaderNodeTexNoise")
    noise.inputs["Scale"].default_value = 60
    noise.inputs["Detail"].default_value = 4
    bump = nodes.new("ShaderNodeBump")
    bump.inputs["Strength"].default_value = 0.08
    links.new(noise.outputs["Fac"], bump.inputs["Height"])
    links.new(bump.outputs["Normal"], bsdf.inputs["Normal"])
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def key_value(socket, frame, value):
    socket.default_value = value
    socket.keyframe_insert("default_value", frame=frame)


def no_shadow(obj):
    obj.visible_shadow = False
    return obj


def make_mesh(name, verts, faces):
    mesh = bpy.data.meshes.new(f"{PREFIX}{name}")
    mesh.from_pydata(verts, [], faces)
    mesh.update()
    return bpy.data.objects.new(f"{PREFIX}{name}", mesh)


def uv_sphere(name, radius, segments=16, rings=8):
    bm_verts = [(0, 0, radius)]
    for ring in range(1, rings):
        phi = math.pi * ring / rings
        for seg in range(segments):
            theta = 2 * math.pi * seg / segments
            bm_verts.append((radius * math.sin(phi) * math.cos(theta), radius * math.sin(phi) * math.sin(theta),
                             radius * math.cos(phi)))
    bm_verts.append((0, 0, -radius))
    faces = [(0, 1 + (s + 1) % segments, 1 + s) for s in range(segments)]
    for ring in range(rings - 2):
        a, b = 1 + ring * segments, 1 + (ring + 1) * segments
        faces += [(a + s, a + (s + 1) % segments, b + (s + 1) % segments, b + s) for s in range(segments)]
    last = len(bm_verts) - 1
    base = 1 + (rings - 2) * segments
    faces += [(base + s, base + (s + 1) % segments, last) for s in range(segments)]
    obj = make_mesh(name, bm_verts, faces)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    return obj


def flat_quad(name, width, height):
    """Quad in the XZ plane, facing -Y, centred on the origin, with UVs."""
    w, h = width / 2, height / 2
    obj = make_mesh(name, [(-w, 0, -h), (w, 0, -h), (w, 0, h), (-w, 0, h)], [(0, 1, 2, 3)])
    uv = obj.data.uv_layers.new(name="UVMap")
    for loop, coord in zip(uv.data, ((0, 0), (1, 0), (1, 1), (0, 1))):
        loop.uv = coord
    return obj


def star_outline(points, outer, inner, phase=math.pi / 2):
    out = []
    for i in range(points * 2):
        radius = outer if i % 2 == 0 else inner
        angle = phase + math.pi * i / points
        out.append((radius * math.cos(angle), radius * math.sin(angle)))
    return out


def extrude_outline(name, outline, depth, axis="Y"):
    """Prism from a convex-ish 2D outline (x, up) drawn on the XZ plane, front
    face toward -Y, depth along +Y. Star outlines use a centre fan, so they
    stay correct even though they are not convex."""
    n = len(outline)
    verts = [(x, 0, z) for x, z in outline] + [(x, depth, z) for x, z in outline]
    verts += [(0, 0, 0), (0, depth, 0)]
    front_c, back_c = 2 * n, 2 * n + 1
    faces = [(i, (i + 1) % n, front_c) for i in range(n)]
    faces += [(n + (i + 1) % n, n + i, back_c) for i in range(n)]
    faces += [(i, n + i, n + (i + 1) % n, (i + 1) % n) for i in range(n)]
    obj = make_mesh(name, verts, faces)
    bevel = obj.modifiers.new("Bevel", "BEVEL")
    bevel.width = min(0.012, depth * 0.45)
    bevel.segments = 2
    bevel.limit_method = "ANGLE"
    return obj


def annulus(name, inner, outer, depth, segments=96):
    """Flat ring on the XZ plane, front face toward -Y, extruded by depth
    along +Y (negative depth extrudes toward -Y, for the card back)."""
    ring = [(math.cos(2 * math.pi * k / segments), math.sin(2 * math.pi * k / segments)) for k in range(segments)]
    verts = []
    for y in (0, depth):
        verts += [(x * outer, y, z * outer) for x, z in ring] + [(x * inner, y, z * inner) for x, z in ring]
    n = segments
    faces = []
    for k in range(n):
        a, b = k, (k + 1) % n
        front = (a, b, n + b, n + a)
        back = (2 * n + a, 2 * n + n + a, 2 * n + n + b, 2 * n + b)
        faces += [front, back]
        faces.append((2 * n + a, 2 * n + b, b, a))  # outer wall
        faces.append((n + a, n + b, 3 * n + b, 3 * n + a))  # inner wall
    if depth < 0:
        faces = [tuple(reversed(f)) for f in faces]
    obj = make_mesh(name, verts, faces)
    bevel = obj.modifiers.new("Bevel", "BEVEL")
    bevel.width = min(0.006, abs(depth) * 0.4)
    bevel.segments = 2
    bevel.limit_method = "ANGLE"
    return obj


def flat_shape(name, loops, depth, material):
    """A flat filled shape from closed polylines (x, up); an inner loop cuts a
    hole. Facing -Y like text, depth thick, with a hint of bevel."""
    curve = bpy.data.curves.new(f"{PREFIX}{name}", "CURVE")
    curve.dimensions = "2D"
    curve.fill_mode = "BOTH"
    curve.extrude = depth / 2
    curve.bevel_depth = min(0.003, depth / 2)
    curve.bevel_resolution = 1
    for loop in loops:
        spline = curve.splines.new("POLY")
        spline.points.add(len(loop) - 1)
        for point, (x, z) in zip(spline.points, loop):
            point.co = (x, z, 0, 1)
        spline.use_cyclic_u = True
    curve.materials.append(material)
    obj = bpy.data.objects.new(f"{PREFIX}{name}", curve)
    obj.rotation_euler = (math.pi / 2, 0, 0)
    return obj


def offset_path(path, width):
    """Left and right edges of a stroke of the given half width along path."""
    left, right = [], []
    for i, (x, z) in enumerate(path):
        ax, az = path[max(0, i - 1)]
        bx, bz = path[min(len(path) - 1, i + 1)]
        dx, dz = bx - ax, bz - az
        length = math.hypot(dx, dz) or 1.0
        nx, nz = -dz / length, dx / length
        w = width * (1 - 0.55 * i / (len(path) - 1))  # tapering to the tip
        left.append((x + nx * w, z + nz * w))
        right.append((x - nx * w, z - nz * w))
    return left, right


def cat_outline():
    """A cat seen from behind, sitting: a pear-shaped body, a round head with
    two ears, and a small tail hanging down and curling. Feet at the origin,
    about 0.7 tall, the tail reaching below."""
    side = [(0.10, 0.0), (0.165, 0.025), (0.19, 0.09), (0.185, 0.18), (0.155, 0.27), (0.11, 0.35),
            (0.085, 0.41), (0.11, 0.455), (0.125, 0.51), (0.12, 0.565), (0.125, 0.665), (0.06, 0.61)]
    right = side
    left = [(-x, z) for x, z in reversed(side)]
    tail = [(0.07, 0.01), (0.085, -0.07), (0.085, -0.15), (0.065, -0.22), (0.02, -0.26), (-0.03, -0.255), (-0.055, -0.22)]
    outer, inner = offset_path(tail, 0.022)
    # Up the right side, over the head, down the left, then out along the
    # tail's left edge and back along its right edge.
    return right + left + [(-0.10, 0.0), (0.03, 0.0)] + inner + list(reversed(outer))


def moon_cat_emblem(size):
    """The pack's emblem: a plain crescent moon opening to the right with a
    cat sitting on its lower horn, inside a diamond frame. Returns
    (name, loops) pieces, size being the moon radius."""
    # Crescent: the outer circle minus an offset inner circle.
    cx, cz, r = 0.2, 0.08, 0.92

    def outside_inner(a):
        x, z = math.cos(a), math.sin(a)
        return (x - cx) ** 2 + (z - cz) ** 2 > r * r

    steps = 720
    angles = [2 * math.pi * k / steps for k in range(steps)]
    start = next(k for k in range(steps) if outside_inner(angles[k]) and not outside_inner(angles[k - 1]))
    outer = []
    k = start
    while outside_inner(angles[k % steps]):
        outer.append((math.cos(angles[k % steps]), math.sin(angles[k % steps])))
        k += 1
    a0 = math.atan2(outer[-1][1] - cz, outer[-1][0] - cx)
    a1 = math.atan2(outer[0][1] - cz, outer[0][0] - cx)
    if a1 > a0:
        a1 -= 2 * math.pi
    inner = [(cx + r * math.cos(lerp(a0, a1, t / 60)), cz + r * math.sin(lerp(a0, a1, t / 60))) for t in range(1, 60)]
    moon = [(x * size, z * size) for x, z in outer + inner]
    # The cat sits on the lower horn, on the inner curve.
    seat_x = 0.5
    seat_z = cz - math.sqrt(r * r - (seat_x - cx) ** 2)
    cat = [((seat_x + x * 1.05) * size, (seat_z - 0.01 + z * 1.05) * size) for x, z in cat_outline()]
    d_out, d_in = size * 1.55, size * 1.47
    diamond = [[(0, d), (d, 0), (0, -d), (-d, 0)] for d in (d_out, d_in)]
    return [("Moon", [moon]), ("Cat", [cat]), ("Diamond", diamond)]


def text_object(name, body, size, depth, material):
    curve = bpy.data.curves.new(f"{PREFIX}{name}", "FONT")
    curve.body = body
    if os.path.exists(SERIF):
        curve.font = bpy.data.fonts.load(SERIF, check_existing=True)
    curve.size = size
    curve.extrude = depth
    curve.bevel_depth = depth * 0.5
    curve.bevel_resolution = 3
    curve.align_x = "CENTER"
    curve.align_y = "CENTER"
    curve.materials.append(material)
    obj = bpy.data.objects.new(f"{PREFIX}{name}", curve)
    # Text lies in XY; stand it up so it reads from -Y.
    obj.rotation_euler = (math.pi / 2, 0, 0)
    return obj


def bake(obj, frames, fn):
    """Keys location, rotation and scale every frame from fn(seconds), which
    returns (location, rotation euler, scale) with None for unchanged parts."""
    for frame in frames:
        location, rotation, scale = fn(seconds_at(frame))
        if location is not None:
            obj.location = location
            obj.keyframe_insert("location", frame=frame)
        if rotation is not None:
            obj.rotation_euler = rotation
            obj.keyframe_insert("rotation_euler", frame=frame)
        if scale is not None:
            obj.scale = scale if isinstance(scale, (tuple, Vector)) else (scale, scale, scale)
            obj.keyframe_insert("scale", frame=frame)
    set_linear(obj)


def bake_matrix(obj, frames, fn):
    """Keys location and rotation every frame from fn(seconds), a world (or
    parent-space) matrix. Each Euler is chosen closest to the last, so a
    whole turn never flips inside the motion blur shutter."""
    previous = None
    for frame in frames:
        location, rotation, _ = fn(seconds_at(frame)).decompose()
        euler = rotation.to_euler("XYZ", previous) if previous is not None else rotation.to_euler("XYZ")
        previous = euler
        obj.location = location
        obj.rotation_euler = euler
        obj.keyframe_insert("location", frame=frame)
        obj.keyframe_insert("rotation_euler", frame=frame)
    set_linear(obj)


def set_linear(obj):
    action = obj.animation_data.action if obj.animation_data else None
    if action is None:
        return
    # Blender 4.4+ keeps F-curves in layered channelbags.
    curves = []
    if hasattr(action, "layers") and action.layers:
        for layer in action.layers:
            for strip in layer.strips:
                for bag in strip.channelbags:
                    curves.extend(bag.fcurves)
    else:
        curves = list(action.fcurves)
    for curve in curves:
        for point in curve.keyframe_points:
            point.interpolation = "LINEAR"


ALL_FRAMES = range(1, LAST_FRAME + 1)


def frames_between(start, end):
    return range(max(1, frame_at(start) - 1), min(LAST_FRAME, frame_at(end) + 1) + 1)


# --- the card -------------------------------------------------------------


def rounded_rect(width, height, radius, segments=8):
    w, h = width / 2 - radius, height / 2 - radius
    points = []
    for corner, (cx, cz) in enumerate(((w, -h), (w, h), (-w, h), (-w, -h))):
        start = -math.pi / 2 + corner * math.pi / 2
        for s in range(segments + 1):
            angle = start + (math.pi / 2) * s / segments
            points.append((cx + radius * math.cos(angle), cz + radius * math.sin(angle)))
    return points


def card_mesh():
    """Rounded slab: material 0 the face (-Y), 1 the back (+Y), 2 the edge."""
    outline = rounded_rect(CARD_W, CARD_H, CARD_RADIUS)
    n = len(outline)
    t = CARD_T / 2
    verts = [(x, -t, z) for x, z in outline] + [(x, t, z) for x, z in outline]
    faces = [tuple(range(n)), tuple(range(2 * n - 1, n - 1, -1))]
    faces += [(i, n + i, n + (i + 1) % n, (i + 1) % n) for i in range(n)]
    obj = make_mesh("Card", verts, faces)
    mesh = obj.data
    uv = mesh.uv_layers.new(name="UVMap")
    for polygon in mesh.polygons:
        polygon.material_index = min(polygon.index, 2)
        for loop_index in polygon.loop_indices:
            x, _, z = mesh.vertices[mesh.loops[loop_index].vertex_index].co
            u = (x + CARD_W / 2) / CARD_W
            if polygon.index == 1:
                u = 1 - u  # the back reads correctly from behind
            uv.data[loop_index].uv = (u, (z + CARD_H / 2) / CARD_H)
    bevel = obj.modifiers.new("Bevel", "BEVEL")
    bevel.width = 0.003
    bevel.segments = 2
    bevel.limit_method = "ANGLE"
    obj.modifiers.new("Normals", "WEIGHTED_NORMAL").keep_sharp = True
    return obj


def card_frame_mask(nodes, links, uv_sep, bands):
    """Mask of thin lines running parallel to the card's rounded edge, at the
    given insets (in card units)."""
    u, v = uv_sep.outputs["X"], uv_sep.outputs["Y"]
    du = math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "MINIMUM", u, math_node(nodes, links, "SUBTRACT", 1.0, u)), CARD_W)
    dv = math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "MINIMUM", v, math_node(nodes, links, "SUBTRACT", 1.0, v)), CARD_H)
    # Rounded distance near the corners: length of the inset vector.
    r = 0.28
    cu = math_node(nodes, links, "MAXIMUM", math_node(nodes, links, "SUBTRACT", r, du), 0.0)
    cv = math_node(nodes, links, "MAXIMUM", math_node(nodes, links, "SUBTRACT", r, dv), 0.0)
    corner = math_node(nodes, links, "SQRT", math_node(nodes, links, "ADD",
                       math_node(nodes, links, "MULTIPLY", cu, cu), math_node(nodes, links, "MULTIPLY", cv, cv)))
    edge = math_node(nodes, links, "MINIMUM", du, dv)
    inset = math_node(nodes, links, "SUBTRACT", r, corner)
    # Near a corner the distance is to the rounded corner, else to the edge.
    near_corner = math_node(nodes, links, "LESS_THAN", edge, r)
    distance = math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", near_corner, math_node(nodes, links, "MAXIMUM", inset, 0.0)),
                         math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "SUBTRACT", 1.0, near_corner), edge))
    total = None
    for center, half in bands:
        offset = math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", distance, center))
        line = map_range(nodes, links, offset, half, half * 0.4)
        total = line if total is None else math_node(nodes, links, "MAXIMUM", total, line)
    return total


def face_material(rarity, palette, tier):
    """Glassy holographic face: a deep gradient, iridescent contour lines that
    shift with the viewing angle, a metal frame, and a keyed shine sweep."""
    material, nodes, links = node_material(f"Face{rarity}")
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    coords = nodes.new("ShaderNodeTexCoord")
    uv_sep = nodes.new("ShaderNodeSeparateXYZ")
    links.new(coords.outputs["UV"], uv_sep.inputs[0])
    u, v = uv_sep.outputs["X"], uv_sep.outputs["Y"]

    # Deep gradient: a glow of the rarity tint low on the card, near black above.
    low = math_node(nodes, links, "POWER", math_node(nodes, links, "SUBTRACT", 1.0, v), 2.2)
    base = mix_rgb(nodes, links, math_node(nodes, links, "MULTIPLY", low, 0.8), palette["deep"], tuple(c * 0.4 for c in palette["tint"]))
    # A darker plate under the label, so the label reads over the pattern.
    left, bottom, right, top = LABEL_BOX
    pu = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", u, 0.5)),
                   (right - left) / CARD_W / 2 + 0.02, (right - left) / CARD_W / 2 - 0.04)
    centre_v = ((top + bottom) / 2 + CARD_H / 2) / CARD_H
    pv = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", v, centre_v)),
                   (top - bottom) / CARD_H / 2 + 0.01, (top - bottom) / CARD_H / 2 - 0.02)
    plate = math_node(nodes, links, "MULTIPLY", pu, pv)
    base = mix_rgb(nodes, links, math_node(nodes, links, "MULTIPLY", plate, 0.75), base, (0.0, 0.0, 0.0))

    # Guilloché, as on a banknote: two families of fine wavy rings around
    # the rank letter that weave into a rosette, fading toward the edges.
    du = math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "SUBTRACT", u, 0.5), CARD_W)
    dv = math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "SUBTRACT", v, 0.68), CARD_H)
    radius = math_node(nodes, links, "SQRT", math_node(nodes, links, "ADD",
                       math_node(nodes, links, "MULTIPLY", du, du), math_node(nodes, links, "MULTIPLY", dv, dv)))
    angle = math_node(nodes, links, "ARCTAN2", dv, du)
    lines = None
    for sign in (1.0, -1.0):
        wave = math_node(nodes, links, "SINE", math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", angle, 14.0),
                                                          math_node(nodes, links, "MULTIPLY", radius, 4.0 * sign)))
        ring = math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", radius, 9.0), math_node(nodes, links, "MULTIPLY", wave, 0.32))
        stripes = math_node(nodes, links, "FRACT", ring)
        family = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", stripes, 0.5)), 0.44, 0.5)
        lines = family if lines is None else math_node(nodes, links, "MAXIMUM", lines, family)
    field = radius
    lines = math_node(nodes, links, "MULTIPLY", lines, map_range(nodes, links, radius, 2.0, 0.3, 0.35, 1.0))
    lines = math_node(nodes, links, "MULTIPLY", lines, math_node(nodes, links, "SUBTRACT", 1.0, math_node(nodes, links, "MULTIPLY", plate, 0.9)))

    # Iridescence: the hue follows position and the viewing angle, so it
    # slides across the card as it tilts.
    weight = nodes.new("ShaderNodeLayerWeight")
    weight.inputs["Blend"].default_value = 0.3
    geometry = nodes.new("ShaderNodeNewGeometry")
    incoming = nodes.new("ShaderNodeSeparateXYZ")
    links.new(geometry.outputs["Incoming"], incoming.inputs[0])
    hue = math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", u, 1.1), math_node(nodes, links, "MULTIPLY", v, 0.7))
    hue = math_node(nodes, links, "ADD", hue, math_node(nodes, links, "MULTIPLY", field, 0.35))
    hue = math_node(nodes, links, "ADD", hue, math_node(nodes, links, "MULTIPLY", incoming.outputs["X"], 2.2))
    hue = math_node(nodes, links, "ADD", hue, math_node(nodes, links, "MULTIPLY", incoming.outputs["Z"], 1.6))
    hue = math_node(nodes, links, "FRACT", math_node(nodes, links, "ADD", hue, math_node(nodes, links, "MULTIPLY", weight.outputs["Facing"], 0.8)))
    rainbow = nodes.new("ShaderNodeCombineColor")
    rainbow.mode = "HSV"
    links.new(hue, rainbow.inputs[0])
    rainbow.inputs[1].default_value = 0.55
    rainbow.inputs[2].default_value = 1.0
    # Muted: the print sits back so the rank, label and stars lead.
    holo = mix_rgb(nodes, links, 0.6, rainbow.outputs[0], palette["metal"])

    # Metal frame: an outer band and a thin inner line.
    frame = card_frame_mask(nodes, links, uv_sep, ((0.1, 0.022), (0.17, 0.006)))

    # Shine sweep: a soft diagonal band whose position is keyed per frame.
    sweep_value = nodes.new("ShaderNodeValue")
    sweep_value.name = "Sweep"
    sweep_value.outputs[0].default_value = -1.0
    diagonal = math_node(nodes, links, "ADD", u, math_node(nodes, links, "MULTIPLY", v, 0.45))
    sweep = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", diagonal, sweep_value.outputs[0])), 0.09, 0.0)

    # The print is also inked faintly into the base, like foil stamping.
    base = mix_rgb(nodes, links, math_node(nodes, links, "MULTIPLY", lines, 0.18), base, palette["metal"])
    color = mix_rgb(nodes, links, frame, base, palette["metal"])
    links.new(color, bsdf.inputs["Base Color"])
    links.new(frame, bsdf.inputs["Metallic"])
    links.new(map_range(nodes, links, frame, 0, 1, 0.32, 0.18, False), bsdf.inputs["Roughness"])
    bsdf.inputs["Coat Weight"].default_value = 1.0
    bsdf.inputs["Coat Roughness"].default_value = 0.04

    line_glow = math_node(nodes, links, "MULTIPLY", lines, 0.22 * tier["holo"])
    # Glitter: fine flecks that catch the light at their own angle, so they
    # twinkle as the card tilts.
    flecks = nodes.new("ShaderNodeTexVoronoi")
    flecks.inputs["Scale"].default_value = 140
    links.new(coords.outputs["UV"], flecks.inputs["Vector"])
    fleck_rgb = nodes.new("ShaderNodeSeparateColor")
    links.new(flecks.outputs["Color"], fleck_rgb.inputs[0])
    angle = math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", incoming.outputs["X"], 9.0),
                      math_node(nodes, links, "MULTIPLY", incoming.outputs["Z"], 7.0))
    phase = math_node(nodes, links, "FRACT", math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", fleck_rgb.outputs[0], 13.0), angle))
    glitter = map_range(nodes, links, phase, 0.965, 1.0)
    glitter = math_node(nodes, links, "MULTIPLY", glitter, map_range(nodes, links, flecks.outputs["Distance"], 0.25, 0.05))
    glitter = math_node(nodes, links, "MULTIPLY", glitter, math_node(nodes, links, "SUBTRACT", 1.0, plate))
    line_glow = math_node(nodes, links, "ADD", line_glow, math_node(nodes, links, "MULTIPLY", glitter, 2.5 * tier["holo"]))
    line_glow = math_node(nodes, links, "MULTIPLY", line_glow, math_node(nodes, links, "SUBTRACT", 1.0, frame))
    emission = holo
    strength = math_node(nodes, links, "ADD", line_glow, math_node(nodes, links, "MULTIPLY", sweep, 1.4))
    # The gilded frame glows faintly, so it reads even with little to reflect.
    strength = math_node(nodes, links, "ADD", strength, math_node(nodes, links, "MULTIPLY", frame, 0.5))
    emission = mix_rgb(nodes, links, frame, emission, palette["metal"])
    links.new(mix_rgb(nodes, links, sweep, emission, (1, 1, 1)), bsdf.inputs["Emission Color"])
    links.new(strength, bsdf.inputs["Emission Strength"])
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def back_material(palette):
    """Midnight back with fine gold diamond lines and a metal frame. The same
    at every rarity, so the back gives nothing away."""
    material, nodes, links = node_material("Back")
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    coords = nodes.new("ShaderNodeTexCoord")
    uv_sep = nodes.new("ShaderNodeSeparateXYZ")
    links.new(coords.outputs["UV"], uv_sep.inputs[0])
    u, v = uv_sep.outputs["X"], uv_sep.outputs["Y"]
    du = math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "SUBTRACT", u, 0.5), CARD_W))
    dv = math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "SUBTRACT", v, 0.5), CARD_H))
    diamond = math_node(nodes, links, "ADD", du, math_node(nodes, links, "MULTIPLY", dv, 0.72))
    rings = math_node(nodes, links, "FRACT", math_node(nodes, links, "MULTIPLY", diamond, 7.0))
    lines = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", rings, 0.5)), 0.43, 0.5)
    # The lines fade toward the centre emblem.
    lines = math_node(nodes, links, "MULTIPLY", lines, map_range(nodes, links, diamond, 0.8, 1.1))
    frame = card_frame_mask(nodes, links, uv_sep, ((0.1, 0.022), (0.17, 0.006)))
    metal = math_node(nodes, links, "MAXIMUM", math_node(nodes, links, "MULTIPLY", lines, 0.8), frame)
    gold = (1.0, 0.74, 0.32)
    links.new(mix_rgb(nodes, links, metal, (0.006, 0.01, 0.03), gold), bsdf.inputs["Base Color"])
    links.new(map_range(nodes, links, metal, 0, 1, 0.3, 1.0, False), bsdf.inputs["Metallic"])
    bsdf.inputs["Roughness"].default_value = 0.25
    # A satin coat: a mirror coat flashes the softboxes white as the card turns.
    bsdf.inputs["Coat Weight"].default_value = 0.4
    bsdf.inputs["Coat Roughness"].default_value = 0.2
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def build_card(scene, rarity, palette, tier):
    card = link(scene, card_mesh())
    card.data.materials.append(face_material(rarity, palette, tier))
    card.data.materials.append(back_material(palette))
    card.data.materials.append(metal_material("Edge", palette["metal"], 0.25))

    gold = metal_material("Gold", palette["metal"], 0.16, palette["glow"], 1.1)
    # The stars glow warm at every rarity, so they read on any face.
    star_gold = metal_material("StarGold", (1.0, 0.8, 0.35), 0.15, (1.0, 0.72, 0.3), 1.6)
    face_y = -CARD_T / 2

    # Text extrudes both ways from its plane, so it sits a full depth out
    # from the face; otherwise it would poke through the back.
    rank = link(scene, text_object("Rank", palette["rank"], 1.6, 0.004, gold))
    rank.data.shear = 0.18
    rank.parent = card
    rank.location = (0, face_y - 0.0065, RANK_Z)

    # Thin rules above and below the label area.
    for index, z in enumerate((LABEL_BOX[3] + 0.02, LABEL_BOX[1] - 0.02)):
        rule = link(scene, extrude_outline(f"Rule{index}", [(-0.78, -0.007), (0.78, -0.007), (0.78, 0.007), (-0.78, 0.007)], 0.003))
        rule.data.materials.append(gold)
        rule.parent = card
        rule.location = (0, face_y - 0.004, z)
        for side in (-1, 1):
            gem = link(scene, extrude_outline(f"RuleGem{index}{side}", star_outline(4, 0.05, 0.016), 0.003))
            gem.data.materials.append(gold)
            gem.parent = card
            gem.location = (side * 0.86, face_y - 0.004, z)

    # Stars, stamped in one by one after the landing.
    stars = []
    spacing = 0.4
    for i in range(rarity):
        star = link(scene, extrude_outline(f"Star{i}", star_outline(5, 0.17, 0.072), 0.005))
        star.data.materials.append(star_gold)
        star.parent = card
        x = (i - (rarity - 1) / 2) * spacing
        appear = STARS_START + i * STAR_STEP

        def star_pose(s, appear=appear, x=x):
            p = progress(s, appear, appear + 0.28)
            pop = 0 if p <= 0 else (1.45 * math.sin(p * math.pi * 0.62) / math.sin(math.pi * 0.62) if p < 0.6 else lerp(1.45, 1.0, smooth((p - 0.6) / 0.4)))
            return (x, face_y - 0.006 - 0.25 * (1 - p) * (p > 0), STARS_Z), (0, 0, (1 - p) * 1.4), max(pop, 0.0001)

        bake(star, frames_between(appear - 0.1, appear + 0.4), star_pose)
        stars.append((star, appear, x))

    # The back carries the moon-and-cat emblem, readable from behind.
    back_gold = metal_material("BackGold", (1.0, 0.74, 0.32), 0.16, (1.0, 0.65, 0.25), 0.25)
    for name, loops in moon_cat_emblem(0.46):
        piece = link(scene, flat_shape(f"Back{name}", loops, 0.003, back_gold))
        piece.parent = card
        piece.location = (0, CARD_T / 2 + 0.0025, 0)
        piece.rotation_euler = (math.pi / 2, 0, math.pi)
    return card, stars


# --- the pack -------------------------------------------------------------


# The right-side strip is a chain of hinged slices stacked along z.
STRIP_SLICES = 18
ROWS_PER_SLICE = 3
# Where the right-side tear runs, starting at a serration notch.
TEAR_X = 1.318


def pack_bulge(x, z):
    """How far the pouch swells out at (x, z); zero at the side seams and
    inside the crimped seals."""
    fx = 1 - (2 * x / PACK_W) ** 6
    inner = PACK_H / 2 - SEAL
    fz = 1 - (min(abs(z), inner) / inner) ** 8
    return PACK_BULGE * max(0.0, fx) * max(0.0, fz)


def crimp(x, z):
    """Ridges pressed into the seals."""
    if abs(z) < PACK_H / 2 - SEAL:
        return 0.0
    return 0.012 * math.sin(x * 48)


def serration(x):
    phase = (x * 22) % 1.0
    return 0.035 * (1 - abs(phase * 2 - 1))


def tear_x(z):
    """The ragged line of the tear down the right side; it wanders more where
    it rips through the stiffer crimped seals."""
    amp = 0.035 if abs(z) > PACK_H / 2 - SEAL else 0.015
    wander = 0.6 * math.sin(z * 41.3 + 0.7) + 0.4 * math.sin(z * 97.1 + 2.1)
    return TEAR_X + amp * wander + 0.01 * math.sin(z * 7)


def front_z(s):
    """Height the tear has reached, running from the top seal down."""
    return PACK_H / 2 + 0.05 - (PACK_H + 0.1) * torn_fraction(s)


TOTAL_ROWS = STRIP_SLICES * ROWS_PER_SLICE


def row_z(j, x_guess):
    """Height of grid row j (0 to TOTAL_ROWS) in a column near x_guess. Body
    and strip use the same rows, so their shared edge matches exactly."""
    bottom = -PACK_H / 2 - serration(x_guess)
    top = PACK_H / 2 + serration(x_guess)
    return lerp(bottom, top, j / TOTAL_ROWS)


def skin(name, x_at, guess_left, guess_right, columns, j0, j1):
    """Front and back foil skins over rows j0 to j1. x_at(t, z) places column
    fraction t at height z. Front faces -Y. UVs span the whole pack, so the
    foil texture runs on across pieces."""
    verts, faces = [], []
    for side in (-1, 1):
        base = len(verts)
        for j in range(j0, j1 + 1):
            for i in range(columns + 1):
                t = i / columns
                z = row_z(j, lerp(guess_left, guess_right, t))
                x = x_at(t, z)
                verts.append((x, side * pack_bulge(x, z) + crimp(x, z), z))
        rows = j1 - j0
        for j in range(rows):
            for i in range(columns):
                a = base + j * (columns + 1) + i
                quad = (a, a + 1, a + columns + 2, a + columns + 1)
                faces.append(quad if side < 0 else tuple(reversed(quad)))
    obj = make_mesh(name, verts, faces)
    uv = obj.data.uv_layers.new(name="UVMap")
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
        for loop_index in polygon.loop_indices:
            x, _, z = obj.data.vertices[obj.data.loops[loop_index].vertex_index].co
            uv.data[loop_index].uv = ((x + PACK_W / 2) / PACK_W, (z + PACK_H / 2) / PACK_H)
    return obj


def foil_material(palette):
    """Crinkled metal foil: dark midnight with a faint oil-slick sheen
    outside, plain matte silver inside."""
    material, nodes, links = node_material("Foil")
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    coords = nodes.new("ShaderNodeTexCoord")
    # Texture space from the UVs, which every piece shares, so the crinkles
    # run on unbroken across the tear.
    space = nodes.new("ShaderNodeVectorMath")
    space.operation = "MULTIPLY"
    space.inputs[1].default_value = (PACK_W, PACK_H, 1.0)
    links.new(coords.outputs["UV"], space.inputs[0])
    noise = nodes.new("ShaderNodeTexNoise")
    noise.inputs["Scale"].default_value = 2.2
    noise.inputs["Detail"].default_value = 8
    noise.inputs["Roughness"].default_value = 0.58
    # Stretched, so the crinkles run like pressed foil wrinkles.
    stretch = nodes.new("ShaderNodeVectorMath")
    stretch.operation = "MULTIPLY"
    stretch.inputs[1].default_value = (0.6, 1.8, 1.0)
    links.new(space.outputs[0], stretch.inputs[0])
    links.new(stretch.outputs[0], noise.inputs["Vector"])
    voronoi = nodes.new("ShaderNodeTexVoronoi")
    voronoi.feature = "DISTANCE_TO_EDGE"
    voronoi.inputs["Scale"].default_value = 3.0
    links.new(space.outputs[0], voronoi.inputs["Vector"])
    creases = map_range(nodes, links, voronoi.outputs["Distance"], 0.0, 0.05)
    height = math_node(nodes, links, "ADD", noise.outputs["Fac"], math_node(nodes, links, "MULTIPLY", creases, 0.1))
    bump = nodes.new("ShaderNodeBump")
    bump.inputs["Strength"].default_value = 0.22
    bump.inputs["Distance"].default_value = 0.03
    links.new(height, bump.inputs["Height"])
    links.new(bump.outputs["Normal"], bsdf.inputs["Normal"])

    uv_sep = nodes.new("ShaderNodeSeparateXYZ")
    links.new(coords.outputs["UV"], uv_sep.inputs[0])
    weight = nodes.new("ShaderNodeLayerWeight")
    weight.inputs["Blend"].default_value = 0.4
    hue = math_node(nodes, links, "FRACT", math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", weight.outputs["Facing"], 1.3),
                                                     math_node(nodes, links, "MULTIPLY", uv_sep.outputs["Y"], 0.4)))
    sheen = nodes.new("ShaderNodeCombineColor")
    sheen.mode = "HSV"
    links.new(hue, sheen.inputs[0])
    sheen.inputs[1].default_value = 0.5
    sheen.inputs[2].default_value = 0.5
    base = mix_rgb(nodes, links, math_node(nodes, links, "MULTIPLY", weight.outputs["Facing"], 0.35), (0.1, 0.15, 0.34), sheen.outputs[0])
    # Gold stripes printed near both seals.
    v = uv_sep.outputs["Y"]
    stripe = None
    for center in (0.065 + SEAL / PACK_H, 1 - 0.065 - SEAL / PACK_H, 0.08 + SEAL / PACK_H, 1 - 0.08 - SEAL / PACK_H):
        band = map_range(nodes, links, math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", v, center)), 0.0035, 0.0015)
        stripe = band if stripe is None else math_node(nodes, links, "MAXIMUM", stripe, band)
    printed = mix_rgb(nodes, links, stripe, base, (1.0, 0.72, 0.3))
    # The inside of the foil is bare silver.
    geometry = nodes.new("ShaderNodeNewGeometry")
    inside = geometry.outputs["Backfacing"]
    links.new(mix_rgb(nodes, links, inside, printed, (0.84, 0.85, 0.88)), bsdf.inputs["Base Color"])
    bsdf.inputs["Metallic"].default_value = 1.0
    roughness = map_range(nodes, links, height, 0.3, 0.8, 0.1, 0.26, False)
    links.new(math_node(nodes, links, "MAXIMUM", roughness, math_node(nodes, links, "MULTIPLY", inside, 0.4)), bsdf.inputs["Roughness"])
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def shaft_material(palette):
    """Light pouring out of the slit: bright at its source, fading outward
    and to the sides."""
    material, nodes, links = node_material("Shaft")
    output = nodes.new("ShaderNodeOutputMaterial")
    coords = nodes.new("ShaderNodeTexCoord")
    sep = nodes.new("ShaderNodeSeparateXYZ")
    links.new(coords.outputs["UV"], sep.inputs[0])
    # The shaft spans only the torn part of the slit: U runs down the pack, and
    # OpenCentre and OpenHalf, keyed per frame, are that part's middle and
    # half length in U.
    centre = nodes.new("ShaderNodeValue")
    centre.name = "OpenCentre"
    half = nodes.new("ShaderNodeValue")
    half.name = "OpenHalf"
    across = math_node(nodes, links, "DIVIDE",
                       math_node(nodes, links, "ABSOLUTE", math_node(nodes, links, "SUBTRACT", sep.outputs["X"], centre.outputs[0])),
                       half.outputs[0])
    # The shaft widens as it travels.
    width = math_node(nodes, links, "ADD", 0.7, math_node(nodes, links, "MULTIPLY", sep.outputs["Y"], 0.3))
    sides = map_range(nodes, links, math_node(nodes, links, "DIVIDE", across, width), 1.0, 0.15)
    fall = math_node(nodes, links, "POWER", math_node(nodes, links, "SUBTRACT", 1.0, sep.outputs["Y"]), 2.4)
    streaks = nodes.new("ShaderNodeTexNoise")
    streaks.inputs["Scale"].default_value = 6.0
    stretch = nodes.new("ShaderNodeVectorMath")
    stretch.operation = "MULTIPLY"
    stretch.inputs[1].default_value = (4.0, 0.25, 1.0)
    links.new(coords.outputs["UV"], stretch.inputs[0])
    links.new(stretch.outputs[0], streaks.inputs["Vector"])
    value = math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "MULTIPLY", sides, fall),
                      map_range(nodes, links, streaks.outputs["Fac"], 0.3, 0.7, 0.45, 1.0))
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = (*[lerp(a, b, 0.5) for a, b in zip(palette["glow"], palette["core"])], 1)
    scale = nodes.new("ShaderNodeMath")
    scale.operation = "MULTIPLY"
    scale.name = "Strength"
    links.new(value, scale.inputs[0])
    scale.inputs[1].default_value = 0.0
    links.new(scale.outputs[0], emission.inputs["Strength"])
    add = nodes.new("ShaderNodeAddShader")
    transparent = nodes.new("ShaderNodeBsdfTransparent")
    links.new(transparent.outputs[0], add.inputs[0])
    links.new(emission.outputs[0], add.inputs[1])
    links.new(add.outputs[0], output.inputs["Surface"])
    material.surface_render_method = "BLENDED"
    return material


# --- choreography shared by the pack, the strip and the card ---------------


def pack_yaw(s):
    """The closed pack winds back a little, turns once about its upright
    axis, overshoots and settles."""
    wind, turn, settle, _ = PACK_TURN
    back = math.radians(-12)
    if s < wind:
        return 0.0
    if s < turn:
        return back * smooth(progress(s, wind, turn))
    if s < settle:
        return lerp(back, 2 * math.pi + math.radians(8), smooth(progress(s, turn, settle)))
    t = s - settle
    return 2 * math.pi + math.radians(8) * math.exp(-t / 0.07) * math.cos(2 * math.pi * t / 0.22)


def pack_slide(s):
    """How far the pack has slid off the card, along its own -X."""
    u = min(2.0, max(0.0, (s - SLIDE_START) / SLIDE_TIME))
    return 6.6 * u ** 1.8


def pack_roll(s):
    """The pack's counter-clockwise turn (from the camera) as it slides off,
    which also tips its path down to the left."""
    return -PACK_ROLL * smooth(clamp01((pack_slide(s) - ROLL_FROM) / (ROLL_TO - ROLL_FROM)))


def pack_base(s):
    """The pack's centre before the tear: a float, a tremble that builds
    toward the tear, and a yank down with each tug."""
    bob = 0.05 * math.sin(s * 2.4)
    tremble = 0.0
    if TREMBLE_START <= s < TEAR:
        amp = 0.02 * smooth(progress(s, TREMBLE_START, TUGS[0][0])) * (1 - progress(s, TEAR - 0.1, TEAR))
        tremble = amp * math.sin(s * 66)
    jolt = sum(-0.08 * math.sin(math.pi * progress(s, start, start + 0.2)) for start, _, _ in TUGS)
    return Vector((tremble * 3, PACK_Y, bob + jolt))


def card_hold(s):
    """Where the card waits after the tear: where the pack let go of it,
    drifting up a little while the pack slides away."""
    held = pack_base(min(s, TEAR))
    held.z += 0.35 * smooth(progress(s, SLIDE_START, SLIDE_START + 0.5))
    return held


def pack_matrix(s):
    """The pack's world matrix. After the tear its position is derived from
    the held card, so it slides off along its own -X, out through the slit,
    and can never cut through the card."""
    sway = 0.03 * math.sin(s * 1.3) * (1 - progress(s, TEAR - 0.3, TEAR))
    roll = pack_roll(s)
    tilt = Matrix.Rotation(sway, 4, "X")
    yaw = Matrix.Rotation(pack_yaw(s), 4, "Z")
    if s < TEAR:
        return Matrix.Translation(pack_base(s)) @ yaw @ tilt
    # The card sits at (distance, 0, 0) in the pack's frame, in its plane, so
    # turning the pack about the view axis can never push it through the foil.
    turned = Matrix.Rotation(roll, 4, "Y") @ yaw
    position = card_hold(s) - turned.to_3x3() @ Vector((pack_slide(s), 0, 0))
    position.y += 0.6 * smooth(progress(s, CARD_CLEAR, PACK_EXIT_END))
    # Once clear, the empty pack turns its torn edge toward the camera.
    swing = Matrix.Rotation(-math.radians(140) * smooth(progress(s, CARD_CLEAR, PACK_EXIT_END)), 4, "Z")
    return Matrix.Translation(position) @ turned @ swing


def build_pack(scene, palette, tier):
    foil = foil_material(palette)
    body = link(scene, skin("PackBody", lambda t, z: lerp(-PACK_W / 2, tear_x(z), t), -PACK_W / 2, TEAR_X,
                            56, 0, TOTAL_ROWS))
    body.data.materials.append(foil)
    bake_matrix(body, ALL_FRAMES, pack_matrix)

    gold = metal_material("PackGold", (1.0, 0.74, 0.32), 0.2, (1.0, 0.65, 0.25), 0.3)
    # The emblem and title are printed on both faces, so the turn shows a
    # dressed back too.
    for face, (y, turn) in enumerate(((-PACK_BULGE - 0.008, 0.0), (PACK_BULGE + 0.008, math.pi))):
        for name, loops in moon_cat_emblem(0.5):
            piece = link(scene, flat_shape(f"Pack{name}{face}", loops, 0.012, gold))
            piece.parent = body
            piece.location = (0, y, 0.3)
            piece.rotation_euler = (math.pi / 2, 0, turn)
        title = link(scene, text_object(f"PackTitle{face}", "WISH", 0.42, 0.006, gold))
        title.data.space_character = 1.4
        title.parent = body
        title.location = (0, y, -0.85)
        title.rotation_euler = (math.pi / 2, 0, turn)

    build_strip(scene, foil, body)

    # The seam glows from inside as the pack trembles, and dims as it tears.
    seam_points = [lerp(-PACK_H / 2, PACK_H / 2, i / 90) for i in range(91)]
    seam_verts, seam_faces = [], []
    for side in (-1, 1):
        base = len(seam_verts)
        for z in seam_points:
            x = tear_x(z)
            y = side * (pack_bulge(x, z) + 0.004)
            seam_verts += [(x - 0.03, y, z), (x + 0.03, y, z)]
        for i in range(len(seam_points) - 1):
            a = base + 2 * i
            seam_faces.append((a, a + 1, a + 3, a + 2))
    seam = link(scene, no_shadow(make_mesh("Seam", seam_verts, seam_faces)))
    seam_mat = glow_material("SeamGlow", palette["core"], 0.0)
    seam.data.materials.append(seam_mat)
    seam.parent = body
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        glow = 5 * smooth(progress(s, TREMBLE_START, TUGS[0][0])) * (1 - torn_fraction(s)) * (0.8 + 0.2 * math.sin(s * 40))
        key_value(strength_socket(seam_mat), frame, glow)

    # Light pours out of the slit: a soft shaft and a spot inside the pouch
    # shining out through it into the haze.
    shaft = link(scene, no_shadow(flat_quad("Shaft", PACK_H * 0.95, 5.0)))
    shaft_mat = shaft_material(palette)
    shaft.data.materials.append(shaft_mat)
    shaft.parent = body
    shaft.location = (TEAR_X + 2.45, -0.05, 0)
    shaft.rotation_euler = (0, math.pi / 2, 0)
    beam = bpy.data.lights.new(f"{PREFIX}Beam", "SPOT")
    beam.color = palette["glow"]
    beam.spot_size = math.radians(70)
    beam.spot_blend = 0.6
    beam.shadow_soft_size = 0.3
    beam.volume_factor = 3.0
    beam_obj = link(scene, bpy.data.objects.new(f"{PREFIX}Beam", beam))
    beam_obj.parent = body
    beam_obj.location = (TEAR_X - 0.7, 0, 0)
    beam_obj.rotation_euler = (0, -math.pi / 2, 0)  # spots shine down -Z; this points it +X
    # The shaft quad's U runs from its top (U 0) to its bottom down the pack.
    shaft_half = PACK_H * 0.95 / 2
    open_centre = shaft_mat.node_tree.nodes["OpenCentre"].outputs[0]
    open_half = shaft_mat.node_tree.nodes["OpenHalf"].outputs[0]
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        # Light comes out only where the foil has torn, from the top seal down
        # to the tear front, and grows with the slit.
        top, front = PACK_H / 2, max(-PACK_H / 2, min(PACK_H / 2, front_z(s)))
        front_u = clamp01((1 - front / shaft_half) / 2)
        key_value(open_centre, frame, front_u / 2)
        key_value(open_half, frame, max(0.01, front_u / 2))
        beam_obj.location.z = (top + front) / 2
        beam_obj.keyframe_insert("location", index=2, frame=frame)
        opened = smooth(clamp01(torn_fraction(s) / 0.4))
        # Out before the slit sweeps across the card, or it would wash it white.
        fade = 1 - smooth(progress(s, SLIDE_START + 0.05, SLIDE_START + 0.35))
        flicker = 0.85 + 0.15 * math.sin(s * 23)
        key_value(strength_socket(shaft_mat), frame, 3.5 * opened * fade * flicker)
        beam.energy = tier["beam"] * opened * fade
        beam.keyframe_insert("energy", frame=frame)

    # A hot point of light rides the tear as it runs down.
    front_mat = glow_material("TearFront", palette["core"], 0.0, falloff=1.0)
    front = link(scene, no_shadow(uv_sphere("TearFront", 0.08, 12, 6)))
    front.data.materials.append(front_mat)
    front.parent = body
    front_light = bpy.data.lights.new(f"{PREFIX}TearLight", "POINT")
    front_light.color = palette["glow"]
    front_light.shadow_soft_size = 0.1
    front_light.specular_factor = 0.4
    front_light_obj = link(scene, bpy.data.objects.new(f"{PREFIX}TearLight", front_light))
    front_light_obj.parent = front
    front_light_obj.location = (0.3, -0.4, 0)

    def front_pose(s):
        z = max(-PACK_H / 2, min(PACK_H / 2, front_z(s)))
        x = tear_x(z)
        return Vector((x, -pack_bulge(x, z) * 0.5, z)), None, None

    bake(front, ALL_FRAMES, front_pose)
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        moving = 0.0
        for start, end, _ in TUGS:
            moving = max(moving, math.sin(math.pi * progress(s, start - 0.02, end + 0.05)))
        key_value(strength_socket(front_mat), frame, 30 * moving)
        front_light.energy = 220 * moving
        front_light.keyframe_insert("energy", frame=frame)
    return body


def build_strip(scene, foil, body):
    """The strip right of the tear, as slices stacked bottom to top. Each
    slice hinges on the one below and tips toward the camera once the tear
    has run past its hinge, so the strip peels down from the top like real
    foil; at TEAR it rips through the bottom seal and flies off right."""
    slices = []
    for k in range(STRIP_SLICES):
        j0, j1 = k * ROWS_PER_SLICE, (k + 1) * ROWS_PER_SLICE
        piece = link(scene, skin(f"PackStrip{k}", lambda t, z: lerp(tear_x(z), PACK_W / 2, t), TEAR_X, PACK_W / 2, 4, j0, j1))
        piece.data.materials.append(foil)
        z = row_z(j0, TEAR_X)
        hinge = Vector((tear_x(z), 0, z))
        piece.data.transform(Matrix.Translation(-hinge))
        slices.append((piece, hinge))

    def bend(s, hinge):
        torn = smooth(clamp01((hinge.z - front_z(s)) / 0.35))
        return Matrix.Rotation(0.17 * torn, 4, "X") @ Matrix.Rotation(0.02 * torn, 4, "Y")

    # The bottom slice carries the rest. It is keyed in world space: riding
    # the pack until the tear, then thrown clear on its own.
    root, root_hinge = slices[0]
    rng = random.Random(3)
    tumble = Vector((rng.uniform(4, 7), rng.uniform(-3, -1.5), rng.uniform(2, 4)))
    at_tear = pack_matrix(TEAR) @ Matrix.Translation(root_hinge) @ bend(TEAR, root_hinge)

    def root_matrix(s):
        if s < TEAR:
            return pack_matrix(s) @ Matrix.Translation(root_hinge) @ bend(s, root_hinge)
        t = s - TEAR
        location, rotation, _ = at_tear.decompose()
        location += Vector((2.2, -1.2, -1.0)) * t + Vector((0, 0, -4.5 * t * t))
        spin = Euler((tumble.x * t, tumble.y * t, tumble.z * t)).to_matrix().to_4x4()
        return Matrix.Translation(location) @ spin @ rotation.to_matrix().to_4x4()

    bake_matrix(root, ALL_FRAMES, root_matrix)
    for k in range(1, STRIP_SLICES):
        piece, hinge = slices[k]
        parent, parent_hinge = slices[k - 1]
        piece.parent = parent
        offset = hinge - parent_hinge
        bake_matrix(piece, ALL_FRAMES, lambda s, offset=offset, hinge=hinge: Matrix.Translation(offset) @ bend(s, hinge))
    # Hide the flying strip once it is well out of shot.
    for piece, _ in slices:
        for frame, hidden in ((frame_at(TEAR + 1.2), False), (frame_at(TEAR + 1.25), True)):
            piece.hide_render = hidden
            piece.keyframe_insert("hide_render", frame=frame)


# --- effects ----------------------------------------------------------------


def build_sparks(scene, palette, tier, seed):
    """Hot sparks spraying out of the slit where the tear runs, from the
    pack's position at that moment."""
    rng = random.Random(seed)
    mat = glow_material("Spark", palette["core"], 10.0, falloff=1.2)
    for i in range(tier["sparks"]):
        spark = link(scene, no_shadow(uv_sphere(f"Spark{i}", 0.018 * rng.uniform(0.6, 1.4), 8, 4)))
        spark.data.materials.append(mat)
        tug = TUGS[i % 2]
        start = rng.uniform(tug[0], tug[1] + 0.03)
        life = rng.uniform(0.5, 1.1)
        z0 = max(-PACK_H / 2, min(PACK_H / 2, front_z(start))) + rng.uniform(-0.06, 0.06)
        origin = pack_matrix(start) @ Vector((tear_x(z0), 0, z0))
        velocity = Vector((rng.uniform(1.5, 5.0), rng.uniform(-2.0, 0.5), rng.uniform(-1.0, 3.0)))

        def pose(s, start=start, life=life, origin=origin, velocity=velocity):
            t = s - start
            if t < 0 or t > life:
                return origin, None, 0.0001
            return origin + velocity * t + Vector((0, 0, -6.0 * t * t)), None, max(0.0001, 1 - (t / life) ** 2)

        bake(spark, frames_between(start - 0.05, start + life + 0.05), pose)


def build_burst(scene, palette, tier, seed):
    """Glittering shards thrown out from the card as it lands."""
    rng = random.Random(seed + 1)
    mats = [glow_material(f"Shard{i}", color, 18.0, falloff=0.8) for i, color in
            enumerate((palette["core"], palette["glow"], (1, 1, 1)))]
    for i in range(tier["burst"]):
        shard = link(scene, no_shadow(extrude_outline(f"Shard{i}", star_outline(4, 0.07, 0.018), 0.004)))
        shard.modifiers.clear()
        shard.data.materials.append(mats[i % len(mats)])
        angle = rng.uniform(0, 2 * math.pi)
        edge = Vector((math.cos(angle) * CARD_W * 0.45, 0, math.sin(angle) * CARD_H * 0.45))
        speed = rng.uniform(1.5, 5.5)
        direction = Vector((math.cos(angle), rng.uniform(-0.5, 0.2), math.sin(angle) * 1.15)).normalized()
        start = LAND + rng.uniform(-0.03, 0.12)
        life = rng.uniform(0.6, 1.4)
        size = rng.uniform(0.5, 1.4)
        twist = rng.uniform(-8, 8)

        def pose(s, start=start, life=life, edge=edge, direction=direction, speed=speed, size=size, twist=twist):
            t = s - start
            if t < 0 or t > life:
                return CARD_REST + edge, None, 0.0001
            travel = speed * (1 - math.exp(-2.6 * t)) / 2.6
            location = CARD_REST + edge + direction * travel + Vector((0, 0, -0.6 * t * t))
            fade = 1 - (t / life)
            return location, Euler((0, twist * t, 0)), size * max(0.0001, fade * (0.6 + 0.4 * math.sin(t * 30 + twist)))

        bake(shard, frames_between(start - 0.05, start + life + 0.05), pose)


def build_shockwaves(scene, palette, tier):
    for i in range(tier["rings"]):
        outline = [(math.cos(a * math.pi / 48), math.sin(a * math.pi / 48)) for a in range(96)]
        verts = [(x, 0, z) for x, z in outline] + [(x * 0.94, 0, z * 0.94) for x, z in outline]
        faces = [(k, (k + 1) % 96, 96 + (k + 1) % 96, 96 + k) for k in range(96)]
        ring = link(scene, no_shadow(make_mesh(f"Wave{i}", verts, faces)))
        mat = glow_material(f"Wave{i}", palette["core"] if i == 0 else palette["glow"], 0.0)
        ring.data.materials.append(mat)
        ring.location = CARD_REST + Vector((0, 0.35, 0))
        start = LAND + i * 0.09
        for frame in frames_between(start - 0.05, start + 0.9):
            s = seconds_at(frame)
            p = progress(s, start, start + 0.8)
            ring.scale = Vector((1, 1, 1)) * (0.3 + 4.5 * ease_out_cubic(p))
            ring.keyframe_insert("scale", frame=frame)
            key_value(strength_socket(mat), frame, 0 if p <= 0 else 12 * (1 - p) ** 1.5)
        set_linear(ring)


def build_embers(scene, palette, tier, seed):
    """Motes drifting up through the haze; more and brighter after the reveal."""
    rng = random.Random(seed + 2)
    dim = glow_material("Dust", (0.75, 0.8, 1.0), 3.0, falloff=1.5)
    hot = glow_material("Ember", palette["core"], 14.0, falloff=1.5)
    total = 26 + tier["embers"]
    for i in range(total):
        after = i >= 26
        mote = link(scene, no_shadow(uv_sphere(f"Mote{i}", rng.uniform(0.008, 0.02) * (1.6 if after else 1), 8, 4)))
        mote.data.materials.append(hot if after else dim)
        x = rng.uniform(-2.2, 2.2)
        y = rng.uniform(-3.0, 3.0)
        z0 = rng.uniform(-4.5, 3.5) if not after else rng.uniform(-4.8, -1.0)
        rise = rng.uniform(0.15, 0.4) if not after else rng.uniform(0.9, 1.8)
        sway = rng.uniform(0.6, 1.6)
        phase = rng.uniform(0, 6.3)
        start = 0.0 if not after else LAND + rng.uniform(-0.1, 1.6)

        def pose(s, x=x, y=y, z0=z0, rise=rise, sway=sway, phase=phase, start=start, after=after):
            t = s - start
            if t < 0:
                return Vector((x, y, z0)), None, 0.0001
            location = Vector((x + 0.15 * math.sin(t * sway + phase), y, z0 + rise * t))
            twinkle = 0.7 + 0.3 * math.sin(t * 7 + phase)
            fade = smooth(clamp01(t / 0.4)) if after else 1.0
            return location, None, max(0.0001, twinkle * fade)

        bake(mote, frames_between(start - 0.05, TOTAL_SECONDS) if after else ALL_FRAMES, pose)


def ray_material(palette, tier):
    """Soft god rays fanning out behind the card; their strength is keyed."""
    material, nodes, links = node_material("Rays")
    output = nodes.new("ShaderNodeOutputMaterial")
    coords = nodes.new("ShaderNodeTexCoord")
    centred = nodes.new("ShaderNodeVectorMath")
    centred.operation = "SUBTRACT"
    centred.inputs[1].default_value = (0.5, 0.5, 0)
    links.new(coords.outputs["UV"], centred.inputs[0])
    sep = nodes.new("ShaderNodeSeparateXYZ")
    links.new(centred.outputs[0], sep.inputs[0])
    angle = math_node(nodes, links, "ARCTAN2", sep.outputs["Y"], sep.outputs["X"])
    length = nodes.new("ShaderNodeVectorMath")
    length.operation = "LENGTH"
    links.new(centred.outputs[0], length.inputs[0])
    distance = length.outputs["Value"]
    rays = None
    for count, weight, offset in ((14, 1.0, 0.0), (23, 0.6, 1.3), (9, 0.5, 2.1)):
        wave = math_node(nodes, links, "SINE", math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", angle, count), offset))
        sharp = math_node(nodes, links, "POWER", math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "ADD", wave, 1.0), 0.5), 6.0)
        sharp = math_node(nodes, links, "MULTIPLY", sharp, weight)
        rays = sharp if rays is None else math_node(nodes, links, "ADD", rays, sharp)
    fade = map_range(nodes, links, distance, 0.5, 0.05)
    core = map_range(nodes, links, distance, 0.22, 0.0)
    value = math_node(nodes, links, "ADD", math_node(nodes, links, "MULTIPLY", rays, fade), math_node(nodes, links, "MULTIPLY", core, 1.5))
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = (*palette["glow"], 1)
    scale = nodes.new("ShaderNodeMath")
    scale.operation = "MULTIPLY"
    scale.name = "Strength"
    links.new(value, scale.inputs[0])
    scale.inputs[1].default_value = 0.0
    links.new(scale.outputs[0], emission.inputs["Strength"])
    add = nodes.new("ShaderNodeAddShader")
    transparent = nodes.new("ShaderNodeBsdfTransparent")
    links.new(transparent.outputs[0], add.inputs[0])
    links.new(emission.outputs[0], add.inputs[1])
    links.new(add.outputs[0], output.inputs["Surface"])
    material.surface_render_method = "BLENDED"
    return material


def build_backdrop(scene, palette, tier):
    """A far wall lit in a soft pool of the rarity colour, the rays, and
    softboxes for the metal and foil to reflect."""
    material, nodes, links = node_material("Wall")
    output = nodes.new("ShaderNodeOutputMaterial")
    coords = nodes.new("ShaderNodeTexCoord")
    centred = nodes.new("ShaderNodeVectorMath")
    centred.operation = "SUBTRACT"
    centred.inputs[1].default_value = (0.5, 0.55, 0)
    links.new(coords.outputs["UV"], centred.inputs[0])
    length = nodes.new("ShaderNodeVectorMath")
    length.operation = "LENGTH"
    links.new(centred.outputs[0], length.inputs[0])
    pool = map_range(nodes, links, length.outputs["Value"], 0.5, 0.0)
    pool_mix = nodes.new("ShaderNodeValue")
    pool_mix.name = "Reveal"
    color = mix_rgb(nodes, links, pool_mix.outputs[0], (0.07, 0.1, 0.22), tuple(c * 0.5 for c in palette["glow"]))
    emission = nodes.new("ShaderNodeEmission")
    links.new(color, emission.inputs["Color"])
    links.new(math_node(nodes, links, "MULTIPLY", math_node(nodes, links, "POWER", pool, 2.2), 0.6), emission.inputs["Strength"])
    links.new(emission.outputs[0], output.inputs["Surface"])
    wall = link(scene, no_shadow(flat_quad("Wall", 22, 30)))
    wall.data.materials.append(material)
    wall.location = (0, 9, 0)
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        key_value(pool_mix.outputs[0], frame, 0.25 * smooth(progress(s, TEAR, TEAR + 0.5)) + 0.75 * smooth(progress(s, LAND - 0.2, LAND + 0.4)))

    rays_mat = ray_material(palette, tier)
    rays = link(scene, no_shadow(flat_quad("Rays", 12, 12)))
    rays.data.materials.append(rays_mat)
    rays.location = CARD_REST + Vector((0, 2.2, 0))
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        p = smooth(progress(s, LAND - 0.15, LAND + 0.35))
        key_value(strength_socket(rays_mat), frame, (0.35 + 1.1 * (1 - progress(s, LAND, LAND + 1.0))) * p * tier["rays"] * 2.2)
        rays.rotation_euler = (0, 0.12 * s, 0)
        rays.keyframe_insert("rotation_euler", frame=frame)
    set_linear(rays)

    # Softboxes out of shot give the metal and foil something to reflect.
    for name, location, size, color, strength in (
            ("BoxLeft", (-4.5, -6.5, 2.5), (1.6, 8), (1.0, 0.97, 0.92), 8.0),
            ("BoxRight", (4.8, -5.5, -0.5), (1.2, 8), (0.85, 0.9, 1.0), 5.0),
            ("BoxTop", (0, -4.0, 6.0), (7, 1.4), palette["glow"], 6.0),
            # Dim and broad, behind the camera: an even sheen on the foil.
            ("BoxBack", (0, -16.0, 2.0), (9, 6), (0.55, 0.6, 0.8), 1.6)):
        box = link(scene, no_shadow(flat_quad(name, *size)))
        mat, box_nodes, box_links = node_material(name)
        emit = box_nodes.new("ShaderNodeEmission")
        emit.inputs["Color"].default_value = (*color, 1)
        emit.inputs["Strength"].default_value = strength
        out = box_nodes.new("ShaderNodeOutputMaterial")
        box_links.new(emit.outputs[0], out.inputs["Surface"])
        box.data.materials.append(mat)
        box.location = location
        box.visible_camera = False
        # Face the scene centre.
        direction = Vector((0, 0, 0)) - Vector(location)
        box.rotation_euler = direction.to_track_quat("-Y", "Z").to_euler()


def build_foil_glint(scene):
    """A bright strip out of shot that slides across, so a highlight runs over
    the foil while the pack waits."""
    strip = link(scene, no_shadow(flat_quad("FoilGlint", 0.9, 12)))
    mat, nodes, links = node_material("FoilGlint")
    emit = nodes.new("ShaderNodeEmission")
    emit.inputs["Color"].default_value = (1.0, 0.97, 0.92, 1)
    emit.inputs["Strength"].default_value = 40.0
    out = nodes.new("ShaderNodeOutputMaterial")
    links.new(emit.outputs[0], out.inputs["Surface"])
    strip.data.materials.append(mat)
    strip.visible_camera = False
    strip.rotation_euler = (0, math.radians(-20), 0)

    def pose(s):
        p = smooth(progress(s, 0.1, 0.95))
        return Vector((lerp(-6.5, 6.5, p), -7.5, 0.6)), None, None

    bake(strip, ALL_FRAMES, pose)


def build_world(scene):
    world = bpy.data.worlds.new(f"{PREFIX}World")
    world.use_nodes = True
    nodes, links = world.node_tree.nodes, world.node_tree.links
    nodes.clear()
    output = nodes.new("ShaderNodeOutputWorld")
    background = nodes.new("ShaderNodeBackground")
    background.inputs["Color"].default_value = (0.012, 0.016, 0.032, 1)
    background.inputs["Strength"].default_value = 1.0
    links.new(background.outputs[0], output.inputs["Surface"])
    # A thin haze, so the lights draw visible beams.
    volume = nodes.new("ShaderNodeVolumePrincipled")
    volume.inputs["Density"].default_value = 0.012
    volume.inputs["Anisotropy"].default_value = 0.45
    links.new(volume.outputs[0], output.inputs["Volume"])
    scene.world = world


def build_lights(scene, palette, tier):
    def light(name, kind, location, energy, color, size=1.0, target=(0, 0, 0)):
        data = bpy.data.lights.new(f"{PREFIX}{name}", kind)
        data.energy = energy
        data.color = color
        if kind == "AREA":
            data.size = size
        elif kind == "SPOT":
            data.spot_size = math.radians(40)
            data.spot_blend = 0.5
            data.shadow_soft_size = size
        else:
            data.shadow_soft_size = size
        obj = link(scene, bpy.data.objects.new(f"{PREFIX}{name}", data))
        obj.location = location
        direction = Vector(target) - Vector(location)
        obj.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()
        return data

    # The main lights mostly light; the softboxes give the shine. A full
    # specular key flashes the glossy card white whenever it turns past it.
    for name, kind, location, energy, color, size in (
            ("Key", "AREA", (-3.5, -6.0, 4.5), 900, (1.0, 0.95, 0.88), 2.5),
            ("Fill", "AREA", (4.0, -5.0, -1.5), 300, (0.75, 0.82, 1.0), 3.0),
            ("Top", "SPOT", (0.5, -1.0, 7.5), 1100, (0.9, 0.92, 1.0), 0.3)):
        light(name, kind, location, energy, color, size).specular_factor = 0.25
    rims = [light(f"Rim{i}", "AREA", (side * 3.0, 3.0, 1.0), 0, palette["glow"], 2.0, (0, 0, 0)) for i, side in enumerate((-1, 1))]
    flash = light("Flash", "POINT", tuple(CARD_REST + Vector((0, -1.5, 0))), 0, palette["core"], 0.5)
    # Diffuse only: a specular flash would burn a hot spot into the glossy face.
    bpy.data.lights[f"{PREFIX}Flash"].specular_factor = 0.0
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        rim = 120 + 700 * smooth(progress(s, LAND - 0.2, LAND + 0.4)) + 300 * smooth(progress(s, TEAR, TEAR + 0.3)) * (1 - progress(s, SLIDE_START, SLIDE_START + 0.4))
        for data in rims:
            data.energy = rim
            data.keyframe_insert("energy", frame=frame)
        burst = math.exp(-((s - LAND) / 0.12) ** 2) * 500 + math.exp(-((s - TEAR - 0.05) / 0.08) ** 2) * 150
        flash.energy = burst
        flash.keyframe_insert("energy", frame=frame)


# --- motion ---------------------------------------------------------------


def shake(s, start, length, amplitude, rate):
    p = progress(s, start, start + length)
    if p <= 0 or p >= 1:
        return Vector((0, 0, 0))
    decay = (1 - p) ** 2 * amplitude
    return Vector((decay * math.sin(s * rate), 0, decay * math.sin(s * rate * 1.37 + 1.1)))


def card_matrix(s):
    """Where the card is. Inside the pack it rides the pack's matrix, back to
    the camera; after the tear it holds still while the pack slides off it,
    upright, then spins 3.5 turns toward the camera and lands face on."""
    if s < TEAR:
        return pack_matrix(s) @ Matrix.Rotation(math.pi, 4, "Z")
    p = progress(s, SPIN_START, LAND)
    glide = ease_out_quart(p)
    location = card_hold(s).lerp(CARD_REST, glide)
    location.z += 0.3 * math.sin(math.pi * glide) * (1 - glide)
    spin = math.pi + 7 * math.pi * ease_out_cubic(p ** 1.25)
    tilt = 0.45 * math.sin(math.pi * p) * (1 - p)
    # A small recoil toward the camera as it lands, then an idle float.
    recoil = math.sin(math.pi * progress(s, LAND, LAND + 0.3)) * (1 - progress(s, LAND, LAND + 0.3))
    location.y -= 0.35 * recoil
    idle = progress(s, LAND, LAND + 0.6)
    t = s - LAND
    if idle > 0:
        location.z += idle * 0.04 * math.sin(t * 1.9)
        tilt += idle * 0.06 * math.sin(t * 1.3 + 0.4)
        spin += idle * 0.13 * math.sin(t * 0.95)
    rotation = Matrix.Rotation(spin, 4, "Z") @ Matrix.Rotation(tilt, 4, "X")
    return Matrix.Translation(location) @ rotation


def animate_card(card):
    bake_matrix(card, ALL_FRAMES, card_matrix)


def animate_sweeps(material):
    socket = material.node_tree.nodes["Sweep"].outputs[0]
    for frame in ALL_FRAMES:
        key_value(socket, frame, sweep_at(seconds_at(frame)))


def sweep_at(s):
    for start, end in SWEEPS:
        if start <= s <= end:
            return lerp(-0.4, 1.9, smooth(progress(s, start, end)))
    return -1.0


def label_opacity(s):
    return smooth(progress(s, *LABEL_IN))


def animate_camera(scene):
    data = bpy.data.cameras.new(f"{PREFIX}Camera")
    data.lens = 50
    data.sensor_fit = "VERTICAL"
    data.sensor_height = 36
    data.dof.use_dof = True
    data.dof.aperture_fstop = 2.8
    camera = link(scene, bpy.data.objects.new(f"{PREFIX}Camera", data))
    scene.camera = camera

    def camera_at(s):
        # A slow push in, a lean toward the tear, then in again for the card.
        y = lerp(CAMERA_START.y, -9.7, smooth(progress(s, 0.0, 1.5)))
        y = lerp(y, CAMERA_HOLD.y, smooth(progress(s, 2.8, LAND)))
        x = 0.25 * smooth(progress(s, 1.5, 2.1)) * (1 - smooth(progress(s, 2.3, 2.8)))
        z = lerp(CAMERA_START.z, CAMERA_HOLD.z, smooth(progress(s, 0.0, LAND)))
        location = Vector((x, y, z))
        location += shake(s, TUGS[0][0], 0.3, 0.03, 66) + shake(s, TUGS[1][1] - 0.05, 0.4, 0.06, 66) + shake(s, LAND, 0.5, 0.07, 64)
        location += Vector((0.05 * math.sin(s * 0.7), 0, 0.03 * math.sin(s * 0.9)))
        return location

    bake(camera, ALL_FRAMES, lambda s: (camera_at(s), Euler((math.pi / 2, 0, 0)), None))
    # Focus follows the pack, then the card.
    for frame in ALL_FRAMES:
        s = seconds_at(frame)
        target_y = PACK_Y if s < SPIN_START else lerp(PACK_Y, CARD_REST.y, ease_out_quart(progress(s, SPIN_START, LAND)))
        data.dof.focus_distance = target_y - camera_at(s).y
        data.dof.keyframe_insert("focus_distance", frame=frame)
    return camera


def setup_compositor(scene, streaks):
    tree = bpy.data.node_groups.new(f"{PREFIX}Compositor", "CompositorNodeTree")
    tree.interface.new_socket("Image", in_out="OUTPUT", socket_type="NodeSocketColor")
    layers = tree.nodes.new("CompositorNodeRLayers")
    layers.scene = scene
    bloom = tree.nodes.new("CompositorNodeGlare")
    bloom.inputs["Type"].default_value = "Bloom"
    bloom.inputs["Threshold"].default_value = 0.7
    bloom.inputs["Size"].default_value = 0.9
    bloom.inputs["Strength"].default_value = 1.0
    tree.links.new(layers.outputs["Image"], bloom.inputs["Image"])
    last = bloom
    if streaks:
        star = tree.nodes.new("CompositorNodeGlare")
        star.inputs["Type"].default_value = "Streaks"
        star.inputs["Threshold"].default_value = 6
        star.inputs["Streaks"].default_value = 4
        star.inputs["Streaks Angle"].default_value = math.radians(45)
        star.inputs["Fade"].default_value = 0.9
        star.inputs["Strength"].default_value = 0.3
        tree.links.new(bloom.outputs["Image"], star.inputs["Image"])
        last = star
    out = tree.nodes.new("NodeGroupOutput")
    tree.links.new(last.outputs["Image"], out.inputs[0])
    scene.compositing_node_group = tree


# --- track export ---------------------------------------------------------


def write_track(scene, camera, card, path):
    """Everything the bot needs to draw over the clip. Per frame: the label
    box and the whole card on screen (corners top-left, top-right,
    bottom-right, bottom-left, in output pixels), how squarely the face points
    at the camera, the sweep position, and the label opacity. Once: the beat
    times, where the pack tears on screen, and the label box in card UVs
    (left, top, right, bottom; v runs up) for matching the sweep."""
    left, bottom, right, top = LABEL_BOX
    face_y = -CARD_T / 2 - 0.002
    w, h = CARD_W / 2, CARD_H / 2

    def quad(matrix, x0, z0, x1, z1):
        out = []
        for x, z in ((x0, z1), (x1, z1), (x1, z0), (x0, z0)):
            ndc = world_to_camera_view(scene, camera, matrix @ Vector((x, face_y, z)))
            out += [round(ndc.x * WIDTH, 2), round((1 - ndc.y) * HEIGHT, 2)]
        return out

    def screen(point):
        ndc = world_to_camera_view(scene, camera, point)
        return [round(ndc.x * WIDTH, 2), round((1 - ndc.y) * HEIGHT, 2)]

    frames = []
    for frame in ALL_FRAMES:
        scene.frame_set(frame)
        matrix = card.matrix_world
        normal = (matrix.to_3x3() @ Vector((0, -1, 0))).normalized()
        centre = matrix @ Vector((0, face_y, (top + bottom) / 2))
        facing = normal.dot((camera.matrix_world.translation - centre).normalized())
        s = seconds_at(frame)
        frames.append({"q": quad(matrix, left, bottom, right, top), "c": quad(matrix, -w, -h, w, h),
                       "f": round(facing, 4), "s": round(sweep_at(s), 4), "a": round(label_opacity(s), 4)})
    # The bot's tear flash goes off at the middle of the slit as it rips free.
    scene.frame_set(frame_at(TEAR))
    body = bpy.data.objects[f"{PREFIX}PackBody"]
    tear_at = screen(body.matrix_world @ Vector((tear_x(0), 0, 0)))
    track = {
        "width": WIDTH, "height": HEIGHT, "fps": FPS, "tear": TEAR, "land": LAND, "tearAt": tear_at,
        "labelUV": [round((left + w) / CARD_W, 4), round((top + h) / CARD_H, 4),
                    round((right + w) / CARD_W, 4), round((bottom + h) / CARD_H, 4)],
        "frames": frames,
    }
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(track, handle, separators=(",", ":"))
        handle.write("\n")


# --- build and render -------------------------------------------------------


def build(rarity, seed=11):
    reset()
    random.seed(seed)
    palette, tier = PALETTES[rarity], TIERS[rarity]
    scene = new_scene()
    build_world(scene)
    build_lights(scene, palette, tier)
    build_backdrop(scene, palette, tier)
    build_foil_glint(scene)
    card, _ = build_card(scene, rarity, palette, tier)
    animate_card(card)
    animate_sweeps(card.data.materials[0])
    build_pack(scene, palette, tier)
    build_sparks(scene, palette, tier, seed)
    build_burst(scene, palette, tier, seed)
    build_shockwaves(scene, palette, tier)
    build_embers(scene, palette, tier, seed)
    camera = animate_camera(scene)
    setup_compositor(scene, tier["streaks"])
    if bpy.context.window is not None:
        bpy.context.window.scene = scene
    return scene, camera, card


def render(scene, camera, card, out_dir, only_frames=None, percent=100):
    os.makedirs(out_dir, exist_ok=True)
    write_track(scene, camera, card, os.path.join(out_dir, "track.json"))
    if only_frames == []:
        return
    scene.render.resolution_percentage = percent
    for frame in only_frames or ALL_FRAMES:
        scene.frame_set(frame)
        scene.render.filepath = os.path.join(out_dir, f"frame-{frame:04d}.png")
        bpy.ops.render.render(write_still=True, scene=scene.name)


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    parser = argparse.ArgumentParser(description="Render the /gacha card-pack wish frames.")
    parser.add_argument("--rarity", type=int, choices=(3, 4, 5), required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--seed", type=int, default=11)
    parser.add_argument("--frames", help="comma-separated frame numbers to render, for a quick look")
    parser.add_argument("--percent", type=int, default=100, help="render scale, for a quick look")
    parser.add_argument("--track-only", action="store_true", help="write track.json and render nothing")
    args = parser.parse_args(argv)
    only = [int(f) for f in args.frames.split(",")] if args.frames else None
    if args.track_only:
        only = []
    scene, camera, card = build(args.rarity, args.seed)
    render(scene, camera, card, args.out, only, args.percent)


if __name__ == "__main__" and bpy.app.background:
    main()
