"""Builds and renders the pre-rendered backdrop of the /genshin meteor wish.

One clip per rarity: a meteor coloured by the rarity falls over a night sea and
strikes a distant ridge, then (hidden under the composition's white flash) the
scene cuts to the 3D rank emblem reveal. The Remotion composition plays the
clip and draws the flash, the label, and the stars on top.

Usage (Blender 4.2 or newer, Eevee):

    blender -b --factory-startup -P renderer/blender/wish-scene.py -- \
        --rarity 5 --out <frames-dir>

Frames land in <frames-dir>/frame-0001.png and up; `encode-wishes.sh` turns
them into renderer/public/genshin/wish-<rarity>.mp4.
"""

import argparse
import math
import random
import sys

import bpy
from mathutils import Vector

FPS = 30
WIDTH, HEIGHT = 854, 480
TOTAL_SECONDS = 7.0
METEOR_START, METEOR_END = 0.3, 2.5
# The scene cuts to the reveal here, while the composition's flash is opaque.
REVEAL_START = 2.95
EMBLEM_SETTLED = 3.7
# Rank emblem centre on screen, as fractions from the top-left corner. Must
# match `emblemCenter` in src/remotion/genshin-timeline.js.
EMBLEM_SCREEN = (0.66, 0.47)

PALETTES = {
    3: {"core": (0.9, 0.95, 1.0), "glow": (0.22, 0.52, 1.0), "deep": (0.03, 0.1, 0.34), "face": (0.02, 0.1, 0.55)},
    4: {"core": (0.96, 0.88, 1.0), "glow": (0.6, 0.3, 1.0), "deep": (0.15, 0.04, 0.36), "face": (0.17, 0.02, 0.48)},
    5: {"core": (1.0, 0.96, 0.82), "glow": (1.0, 0.62, 0.15), "deep": (0.3, 0.15, 0.02), "face": (0.55, 0.22, 0.01)},
}

# How loud each tier is; every beat exists at every tier.
TIERS = {
    3: {"head": 1.0, "sparks": 14, "light": 1.0, "rays": 0.22, "counter": False, "motes": 16, "rain": 0, "rings": 1, "streaks": False, "sweep": False},
    4: {"head": 1.3, "sparks": 36, "light": 2.2, "rays": 0.38, "counter": False, "motes": 32, "rain": 0, "rings": 2, "streaks": True, "sweep": False},
    5: {"head": 1.75, "sparks": 80, "light": 4.5, "rays": 0.55, "counter": True, "motes": 56, "rain": 40, "rings": 3, "streaks": True, "sweep": True},
}

PREFIX = "Wish"
FONT_PATHS = ["/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"]


def frame_at(seconds):
    """Blender frame number (1-based) shown at `seconds` into the clip."""
    return 1 + round(seconds * FPS)


def seconds_at(frame):
    return (frame - 1) / FPS


LAST_FRAME = frame_at(TOTAL_SECONDS) - 1
CUT_FRAME = math.ceil(REVEAL_START * FPS) + 1


def ease_in_quad(t):
    return t * t


def ease_out_cubic(t):
    return 1 - (1 - t) ** 3


def clamp01(value):
    return min(1.0, max(0.0, value))


def progress(seconds, start, end):
    return clamp01((seconds - start) / (end - start))


# --- scene plumbing -------------------------------------------------------


def remove_previous():
    """Deletes the scenes and datablocks a previous build created."""
    for scene in [s for s in bpy.data.scenes if s.name.startswith(PREFIX)]:
        for obj in list(scene.objects):
            bpy.data.objects.remove(obj, do_unlink=True)
        bpy.data.scenes.remove(scene)
    for collection in (bpy.data.meshes, bpy.data.materials, bpy.data.lights, bpy.data.cameras,
                       bpy.data.curves, bpy.data.worlds, bpy.data.node_groups, bpy.data.lightprobes):
        for block in [b for b in collection if b.name.startswith(PREFIX) and b.users == 0]:
            collection.remove(block)


def new_scene(name, frame_start, frame_end):
    scene = bpy.data.scenes.new(f"{PREFIX}{name}")
    scene.render.engine = "BLENDER_EEVEE"
    scene.render.resolution_x = WIDTH
    scene.render.resolution_y = HEIGHT
    scene.render.resolution_percentage = 100
    scene.render.fps = FPS
    scene.render.image_settings.file_format = "PNG"
    scene.render.image_settings.color_mode = "RGB"
    scene.frame_start = frame_start
    scene.frame_end = frame_end
    scene.eevee.taa_render_samples = 32
    scene.eevee.use_raytracing = True
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


def glow_material(name, color, strength, falloff=True, opaque=False):
    """Additive emission with a soft edge, for glows that should not occlude.

    An opaque glow is plain emission, which reflections can capture.
    """
    material, nodes, links = node_material(name)
    output = nodes.new("ShaderNodeOutputMaterial")
    add = nodes.new("ShaderNodeAddShader")
    transparent = nodes.new("ShaderNodeBsdfTransparent")
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = (*color, 1)
    emission.inputs["Strength"].default_value = strength
    if falloff:
        # Bright where the surface faces the camera, fading at the silhouette.
        weight = nodes.new("ShaderNodeLayerWeight")
        weight.inputs["Blend"].default_value = 0.5
        invert = nodes.new("ShaderNodeMath")
        invert.operation = "SUBTRACT"
        invert.inputs[0].default_value = 1
        links.new(weight.outputs["Facing"], invert.inputs[1])
        power = nodes.new("ShaderNodeMath")
        power.operation = "POWER"
        power.inputs[1].default_value = 2.5
        links.new(invert.outputs[0], power.inputs[0])
        scale = nodes.new("ShaderNodeMath")
        scale.operation = "MULTIPLY"
        scale.inputs[1].default_value = strength
        scale.name = "StrengthScale"
        links.new(power.outputs[0], scale.inputs[0])
        links.new(scale.outputs[0], emission.inputs["Strength"])
    links.new(transparent.outputs[0], add.inputs[0])
    links.new(emission.outputs[0], add.inputs[1])
    if opaque:
        links.new(emission.outputs[0], output.inputs["Surface"])
        material.surface_render_method = "DITHERED"
    else:
        links.new(add.outputs[0], output.inputs["Surface"])
        material.surface_render_method = "BLENDED"
    return material


def strength_socket(material):
    """Socket that sets a glow material's overall brightness."""
    nodes = material.node_tree.nodes
    if "StrengthScale" in nodes:
        return nodes["StrengthScale"].inputs[1]
    return next(n for n in nodes if n.bl_idname == "ShaderNodeEmission").inputs["Strength"]


def mix_color(nodes, blend_type):
    """Mix node in colour mode, returning it with its colour A, B and result sockets."""
    mix = nodes.new("ShaderNodeMix")
    mix.data_type = "RGBA"
    mix.blend_type = blend_type
    mix.inputs["Factor"].default_value = 1
    a = next(i for i in mix.inputs if i.identifier == "A_Color")
    b = next(i for i in mix.inputs if i.identifier == "B_Color")
    result = next(o for o in mix.outputs if o.identifier == "Result_Color")
    return mix, a, b, result


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


def uv_sphere(name, radius, segments=24, rings=12):
    verts = [(0, 0, radius)]
    for ring in range(1, rings):
        phi = math.pi * ring / rings
        for seg in range(segments):
            theta = 2 * math.pi * seg / segments
            verts.append((radius * math.sin(phi) * math.cos(theta), radius * math.sin(phi) * math.sin(theta), radius * math.cos(phi)))
    verts.append((0, 0, -radius))
    faces = [(0, 1 + (s + 1) % segments, 1 + s) for s in range(segments)]
    for ring in range(rings - 2):
        a, b = 1 + ring * segments, 1 + (ring + 1) * segments
        faces += [(a + s, a + (s + 1) % segments, b + (s + 1) % segments, b + s) for s in range(segments)]
    last = len(verts) - 1
    base = 1 + (rings - 2) * segments
    faces += [(base + s, base + (s + 1) % segments, last) for s in range(segments)]
    obj = make_mesh(name, verts, faces)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    return obj


def cone(name, radius, segments=32):
    """Unit-length cone along -Z: its base circle sits at the origin."""
    verts = [(radius * math.cos(2 * math.pi * s / segments), radius * math.sin(2 * math.pi * s / segments), 0) for s in range(segments)]
    verts.append((0, 0, -1))
    tip = len(verts) - 1
    faces = [(s, (s + 1) % segments, tip) for s in range(segments)]
    obj = make_mesh(name, verts, faces)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    return obj


def value_noise(seed):
    def hash2(i, j):
        n = (i * 374761393 + j * 668265263 + seed * 2246822519) & 0xFFFFFFFF
        n = ((n ^ (n >> 13)) * 1274126177) & 0xFFFFFFFF
        return ((n ^ (n >> 16)) & 0xFFFF) / 65535.0

    def noise(x, y):
        i, j = math.floor(x), math.floor(y)
        tx, ty = x - i, y - j
        sx, sy = tx * tx * (3 - 2 * tx), ty * ty * (3 - 2 * ty)
        top = hash2(i, j) + (hash2(i + 1, j) - hash2(i, j)) * sx
        bottom = hash2(i, j + 1) + (hash2(i + 1, j + 1) - hash2(i, j + 1)) * sx
        return top + (bottom - top) * sy

    def fbm(x, y, octaves=6):
        total, amplitude, frequency = 0.0, 0.5, 1.0
        for _ in range(octaves):
            total += amplitude * noise(x * frequency, y * frequency)
            amplitude *= 0.5
            frequency *= 2.03
        return total

    return fbm


def heightfield(name, x_range, y_range, steps, height_fn):
    (x0, x1), (y0, y1), (nx, ny) = x_range, y_range, steps
    verts = []
    for j in range(ny + 1):
        y = y0 + (y1 - y0) * j / ny
        for i in range(nx + 1):
            x = x0 + (x1 - x0) * i / nx
            verts.append((x, y, height_fn(x, y)))
    faces = [(j * (nx + 1) + i, j * (nx + 1) + i + 1, (j + 1) * (nx + 1) + i + 1, (j + 1) * (nx + 1) + i)
             for j in range(ny) for i in range(nx)]
    obj = make_mesh(name, verts, faces)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    return obj


def rock_material(name, color, rim):
    material, nodes, links = node_material(name)
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    bsdf.inputs["Base Color"].default_value = (*color, 1)
    bsdf.inputs["Roughness"].default_value = 0.85
    # A faint self-glow keeps distant rock readable as haze against the sky.
    bsdf.inputs["Emission Color"].default_value = (*rim, 1)
    bsdf.inputs["Emission Strength"].default_value = 1
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def setup_compositor(scene, name, streaks):
    tree = bpy.data.node_groups.new(f"{PREFIX}{name}Compositor", "CompositorNodeTree")
    tree.interface.new_socket("Image", in_out="OUTPUT", socket_type="NodeSocketColor")
    layers = tree.nodes.new("CompositorNodeRLayers")
    layers.scene = scene
    bloom = tree.nodes.new("CompositorNodeGlare")
    bloom.inputs["Type"].default_value = "Bloom"
    bloom.inputs["Threshold"].default_value = 0.8
    bloom.inputs["Size"].default_value = 0.85
    bloom.inputs["Strength"].default_value = 0.9
    tree.links.new(layers.outputs["Image"], bloom.inputs["Image"])
    last = bloom
    if streaks:
        star = tree.nodes.new("CompositorNodeGlare")
        star.inputs["Type"].default_value = "Streaks"
        star.inputs["Threshold"].default_value = 25
        star.inputs["Streaks"].default_value = 4
        star.inputs["Streaks Angle"].default_value = 0
        star.inputs["Fade"].default_value = 0.93
        star.inputs["Strength"].default_value = 0.35
        tree.links.new(bloom.outputs["Image"], star.inputs["Image"])
        last = star
    out = tree.nodes.new("NodeGroupOutput")
    tree.links.new(last.outputs["Image"], out.inputs[0])
    scene.compositing_node_group = tree


# --- meteor scene ---------------------------------------------------------


def sky_world(name, seed):
    world = bpy.data.worlds.new(f"{PREFIX}{name}")
    if world.node_tree is None:
        world.use_nodes = True
    nodes, links = world.node_tree.nodes, world.node_tree.links
    nodes.clear()
    output = nodes.new("ShaderNodeOutputWorld")
    background = nodes.new("ShaderNodeBackground")
    coords = nodes.new("ShaderNodeTexCoord")
    direction = coords.outputs["Generated"]

    split = nodes.new("ShaderNodeSeparateXYZ")
    links.new(direction, split.inputs[0])
    gradient = nodes.new("ShaderNodeValToRGB")
    ramp = gradient.color_ramp
    ramp.elements[0].position = 0.0
    ramp.elements[0].color = (0.16, 0.2, 0.48, 1)
    ramp.elements[1].position = 0.55
    ramp.elements[1].color = (0.004, 0.006, 0.03, 1)
    middle = ramp.elements.new(0.12)
    middle.color = (0.045, 0.06, 0.2, 1)
    links.new(split.outputs["Z"], gradient.inputs["Fac"])

    # Twinkling stars: Voronoi cell centres, thinned by each cell's random
    # value and flickered by a slowly drifting 4D noise.
    def star_layer(scale, keep, size, brightness):
        voronoi = nodes.new("ShaderNodeTexVoronoi")
        voronoi.voronoi_dimensions = "3D"
        voronoi.inputs["Scale"].default_value = scale
        voronoi.inputs["Randomness"].default_value = 1
        links.new(direction, voronoi.inputs["Vector"])
        point = nodes.new("ShaderNodeMapRange")
        point.inputs["From Min"].default_value = 0
        point.inputs["From Max"].default_value = size
        point.inputs["To Min"].default_value = 1
        point.inputs["To Max"].default_value = 0
        links.new(voronoi.outputs["Distance"], point.inputs["Value"])
        pick = nodes.new("ShaderNodeSeparateColor")
        links.new(voronoi.outputs["Color"], pick.inputs[0])
        select = nodes.new("ShaderNodeMath")
        select.operation = "GREATER_THAN"
        select.inputs[1].default_value = keep
        links.new(pick.outputs[0], select.inputs[0])
        twinkle = nodes.new("ShaderNodeTexNoise")
        twinkle.noise_dimensions = "4D"
        twinkle.inputs["Scale"].default_value = scale * 0.5
        twinkle.inputs["W"].default_value = seed
        twinkle.inputs["W"].keyframe_insert("default_value", frame=1)
        twinkle.inputs["W"].default_value = seed + 6
        twinkle.inputs["W"].keyframe_insert("default_value", frame=LAST_FRAME)
        links.new(direction, twinkle.inputs["Vector"])
        flicker = nodes.new("ShaderNodeMapRange")
        flicker.inputs["From Min"].default_value = 0.35
        flicker.inputs["From Max"].default_value = 0.7
        flicker.inputs["To Min"].default_value = 0.15
        flicker.inputs["To Max"].default_value = brightness
        links.new(twinkle.outputs["Fac"], flicker.inputs["Value"])
        stars = nodes.new("ShaderNodeMath")
        stars.operation = "MULTIPLY"
        links.new(point.outputs[0], stars.inputs[0])
        links.new(select.outputs[0], stars.inputs[1])
        lit = nodes.new("ShaderNodeMath")
        lit.operation = "MULTIPLY"
        links.new(stars.outputs[0], lit.inputs[0])
        links.new(flicker.outputs[0], lit.inputs[1])
        return lit

    small = star_layer(260, 0.5, 0.09, 7)
    large = star_layer(80, 0.9, 0.07, 30)

    # A faint galactic band across the sky.
    band_dot = nodes.new("ShaderNodeVectorMath")
    band_dot.operation = "DOT_PRODUCT"
    band_dot.inputs[1].default_value = Vector((0.55, -0.35, 0.76)).normalized()
    links.new(direction, band_dot.inputs[0])
    band_abs = nodes.new("ShaderNodeMath")
    band_abs.operation = "ABSOLUTE"
    links.new(band_dot.outputs["Value"], band_abs.inputs[0])
    band_fall = nodes.new("ShaderNodeMapRange")
    band_fall.inputs["From Min"].default_value = 0
    band_fall.inputs["From Max"].default_value = 0.22
    band_fall.inputs["To Min"].default_value = 1
    band_fall.inputs["To Max"].default_value = 0
    links.new(band_abs.outputs[0], band_fall.inputs["Value"])
    band_noise = nodes.new("ShaderNodeTexNoise")
    band_noise.inputs["Scale"].default_value = 4
    band_noise.inputs["Detail"].default_value = 8
    band_noise.inputs["Roughness"].default_value = 0.62
    links.new(direction, band_noise.inputs["Vector"])
    band_shape = nodes.new("ShaderNodeMapRange")
    band_shape.inputs["From Min"].default_value = 0.42
    band_shape.inputs["From Max"].default_value = 0.75
    links.new(band_noise.outputs["Fac"], band_shape.inputs["Value"])
    band = nodes.new("ShaderNodeMath")
    band.operation = "MULTIPLY"
    links.new(band_fall.outputs[0], band.inputs[0])
    links.new(band_shape.outputs[0], band.inputs[1])
    band_color, band_a, band_b, band_result = mix_color(nodes, "ADD")
    links.new(band.outputs[0], band_color.inputs["Factor"])
    links.new(gradient.outputs["Color"], band_a)
    band_b.default_value = (0.12, 0.1, 0.3, 1)

    starlight = nodes.new("ShaderNodeMath")
    starlight.operation = "ADD"
    links.new(small.outputs[0], starlight.inputs[0])
    links.new(large.outputs[0], starlight.inputs[1])
    # Hide stars below the horizon line and fade them in the horizon haze.
    above = nodes.new("ShaderNodeMapRange")
    above.inputs["From Min"].default_value = 0.0
    above.inputs["From Max"].default_value = 0.12
    links.new(split.outputs["Z"], above.inputs["Value"])
    visible = nodes.new("ShaderNodeMath")
    visible.operation = "MULTIPLY"
    links.new(starlight.outputs[0], visible.inputs[0])
    links.new(above.outputs[0], visible.inputs[1])
    star_color, star_a, star_b, star_result = mix_color(nodes, "ADD")
    links.new(visible.outputs[0], star_color.inputs["Factor"])
    links.new(band_result, star_a)
    star_b.default_value = (0.85, 0.9, 1.0, 1)

    links.new(star_result, background.inputs["Color"])
    background.inputs["Strength"].default_value = 1
    links.new(background.outputs[0], output.inputs["Surface"])
    return world


def sea_material():
    material, nodes, links = node_material("Sea")
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    bsdf.inputs["Base Color"].default_value = (0.002, 0.005, 0.016, 1)
    bsdf.inputs["Roughness"].default_value = 0.12
    bsdf.inputs["IOR"].default_value = 1.33
    coords = nodes.new("ShaderNodeTexCoord")
    mapping = nodes.new("ShaderNodeMapping")
    mapping.inputs["Scale"].default_value = (0.05, 0.2, 1)
    links.new(coords.outputs["Object"], mapping.inputs["Vector"])
    waves = nodes.new("ShaderNodeTexNoise")
    waves.noise_dimensions = "4D"
    waves.inputs["Scale"].default_value = 2
    waves.inputs["Detail"].default_value = 6
    key_value(waves.inputs["W"], 1, 0)
    key_value(waves.inputs["W"], LAST_FRAME, 1.4)
    links.new(mapping.outputs[0], waves.inputs["Vector"])
    bump = nodes.new("ShaderNodeBump")
    bump.inputs["Strength"].default_value = 0.6
    links.new(waves.outputs["Fac"], bump.inputs["Height"])
    links.new(bump.outputs[0], bsdf.inputs["Normal"])
    links.new(bsdf.outputs[0], output.inputs["Surface"])
    return material


def glint_path_material(color):
    """Broken horizontal ripples of light, brightest at the far end."""
    material, nodes, links = node_material("Glint")
    output = nodes.new("ShaderNodeOutputMaterial")
    add = nodes.new("ShaderNodeAddShader")
    transparent = nodes.new("ShaderNodeBsdfTransparent")
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = (*color, 1)
    coords = nodes.new("ShaderNodeTexCoord")
    split = nodes.new("ShaderNodeSeparateXYZ")
    links.new(coords.outputs["Object"], split.inputs[0])
    mapping = nodes.new("ShaderNodeMapping")
    mapping.inputs["Scale"].default_value = (40, 900, 1)
    links.new(coords.outputs["Object"], mapping.inputs["Vector"])
    ripples = nodes.new("ShaderNodeTexNoise")
    ripples.noise_dimensions = "4D"
    ripples.inputs["Detail"].default_value = 3
    key_value(ripples.inputs["W"], 1, 0)
    key_value(ripples.inputs["W"], LAST_FRAME, 6)
    links.new(mapping.outputs[0], ripples.inputs["Vector"])
    sparkle = nodes.new("ShaderNodeMapRange")
    sparkle.inputs["From Min"].default_value = 0.5
    sparkle.inputs["From Max"].default_value = 0.72
    links.new(ripples.outputs["Fac"], sparkle.inputs["Value"])
    # Fade across the path's width and toward the camera.
    width = nodes.new("ShaderNodeMath")
    width.operation = "DIVIDE"
    links.new(split.outputs["X"], width.inputs[0])
    links.new(split.outputs["Y"], width.inputs[1])
    edge = nodes.new("ShaderNodeMapRange")
    edge.inputs["From Min"].default_value = 0
    edge.inputs["From Max"].default_value = 0.05
    edge.inputs["To Min"].default_value = 1
    edge.inputs["To Max"].default_value = 0
    edge_abs = nodes.new("ShaderNodeMath")
    edge_abs.operation = "ABSOLUTE"
    links.new(width.outputs[0], edge_abs.inputs[0])
    links.new(edge_abs.outputs[0], edge.inputs["Value"])
    near = nodes.new("ShaderNodeMath")
    near.operation = "POWER"
    near.inputs[1].default_value = 3
    links.new(split.outputs["Y"], near.inputs[0])
    glint = nodes.new("ShaderNodeMath")
    glint.operation = "MULTIPLY"
    links.new(sparkle.outputs[0], glint.inputs[0])
    links.new(edge.outputs[0], glint.inputs[1])
    glint_far = nodes.new("ShaderNodeMath")
    glint_far.operation = "MULTIPLY"
    links.new(glint.outputs[0], glint_far.inputs[0])
    links.new(near.outputs[0], glint_far.inputs[1])
    scale = nodes.new("ShaderNodeMath")
    scale.operation = "MULTIPLY"
    scale.name = "StrengthScale"
    links.new(glint_far.outputs[0], scale.inputs[0])
    links.new(scale.outputs[0], emission.inputs["Strength"])
    links.new(transparent.outputs[0], add.inputs[0])
    links.new(emission.outputs[0], add.inputs[1])
    links.new(add.outputs[0], output.inputs["Surface"])
    material.surface_render_method = "BLENDED"
    return material


def screen_ray(camera, sx, sy):
    """World direction through a screen point given as fractions from the top-left."""
    half_w = camera.data.sensor_width / 2 / camera.data.lens
    half_h = half_w * HEIGHT / WIDTH
    local = Vector(((sx - 0.5) * 2 * half_w, (0.5 - sy) * 2 * half_h, -1))
    return (camera.rotation_euler.to_matrix() @ local).normalized()


def build_meteor_scene(rarity, seed):
    palette, tier = PALETTES[rarity], TIERS[rarity]
    scene = new_scene("Meteor", 1, CUT_FRAME - 1)
    scene.world = sky_world("Sky", seed)
    scene.render.use_motion_blur = True
    scene.render.motion_blur_shutter = 0.5
    setup_compositor(scene, "Meteor", tier["streaks"])

    camera_data = bpy.data.cameras.new(f"{PREFIX}MeteorCamera")
    camera_data.lens = 28
    camera_data.sensor_fit = "HORIZONTAL"
    camera_data.clip_end = 8000
    camera = link(scene, bpy.data.objects.new(f"{PREFIX}MeteorCamera", camera_data))
    camera.rotation_euler = (math.radians(93.4), 0, 0)
    final_location = Vector((0, -26, 4))
    scene.camera = camera
    for frame, y in ((1, -32.0), (CUT_FRAME - 1, final_location.y)):
        camera.location = (0, y, 4)
        camera.keyframe_insert("location", frame=frame)
    camera.location = final_location

    sea = link(scene, make_mesh("Sea", [(-4000, -100, 0), (4000, -100, 0), (4000, 6000, 0), (-4000, 6000, 0)], [(0, 1, 2, 3)]))
    sea.data.materials.append(sea_material())

    impact = final_location + screen_ray(camera, *EMBLEM_SCREEN) * 1050
    start = final_location + screen_ray(camera, -0.18, -0.3) * 980

    fbm = value_noise(seed)
    ridge_rock = rock_material("RidgeRock", (0.01, 0.012, 0.03), (0.012, 0.016, 0.05))

    def ridge_height(x, y):
        envelope = 0.55 + 0.45 * math.exp(-(((x - impact.x) / 420) ** 2))
        depth = clamp01((y - 1080) / 60) * clamp01((1700 - y) / 200)
        return -6 + depth * envelope * 230 * fbm(x / 260, y / 260) ** 1.6

    ridge = link(scene, heightfield("Ridge", (-2200, 2200), (1080, 1700), (220, 30), ridge_height))
    ridge.data.materials.append(ridge_rock)

    cliff_rock = rock_material("CliffRock", (0.006, 0.007, 0.012), (0.0, 0.0, 0.0))

    def cliff_height(x, y):
        shore = clamp01((-14 - x) / 30) * clamp01((y - 35) / 8) * clamp01((170 - y) / 40)
        return -3 + shore * (6 + 16 * fbm(x / 22 + 40, y / 22)) * (0.6 + 0.4 * clamp01((-x - 30) / 40))

    cliff = link(scene, heightfield("Cliff", (-140, -6), (30, 180), (90, 90), cliff_height))
    cliff.data.materials.append(cliff_rock)

    # Meteor head, glow, trail, and the light it casts on the sea and rock.
    head_radius = 7 * tier["head"]
    head = link(scene, no_shadow(uv_sphere("MeteorHead", head_radius)))
    head.data.materials.append(glow_material("MeteorCore", palette["core"], 40, falloff=False, opaque=True))
    halo = link(scene, no_shadow(uv_sphere("MeteorHalo", head_radius * 3.2)))
    halo.data.materials.append(glow_material("MeteorHalo", palette["glow"], 6))
    halo.parent = head

    back = (start - impact).normalized()
    trail_rotation = back.to_track_quat("-Z", "Y").to_euler()
    trail_core = link(scene, no_shadow(cone("TrailCore", head_radius * 0.9)))
    trail_core.data.materials.append(glow_material("TrailCore", tuple((c + g) / 2 for c, g in zip(palette["core"], palette["glow"])), 8))
    trail_glow = link(scene, no_shadow(cone("TrailGlow", head_radius * 2.6)))
    trail_glow.data.materials.append(glow_material("TrailGlow", palette["glow"], 3.5))
    for trail in (trail_core, trail_glow):
        trail.rotation_euler = trail_rotation

    # The meteor's light glittering on the sea, from the camera out to the
    # point under the head, as moonlight does on water.
    glint_material = glint_path_material(palette["glow"])
    glint = link(scene, no_shadow(make_mesh("Glint", [(-0.004, 0.02, 0), (0.004, 0.02, 0), (0.05, 1, 0), (-0.05, 1, 0)], [(0, 1, 2, 3)])))
    glint.data.materials.append(glint_material)
    glint_strength = strength_socket(glint_material)

    light_data = bpy.data.lights.new(f"{PREFIX}MeteorLight", "POINT")
    light_data.color = palette["glow"]
    light_data.shadow_soft_size = 12
    # The sea mirrors the glowing head itself; a specular hotspot from the
    # light would sit in the wrong place.
    light_data.specular_factor = 0
    light = link(scene, bpy.data.objects.new(f"{PREFIX}MeteorLight", light_data))

    path_length = (impact - start).length
    for frame in range(1, CUT_FRAME):
        seconds = seconds_at(frame)
        flight = ease_in_quad(progress(seconds, METEOR_START, METEOR_END))
        visible = METEOR_START <= seconds
        position = start.lerp(impact, flight)
        size = (0.4 + 0.6 * flight) if visible else 0.0001
        head.location = position
        head.scale = (size, size, size)
        head.keyframe_insert("location", frame=frame)
        head.keyframe_insert("scale", frame=frame)
        length = max(0.001, path_length * flight * 0.75) if visible else 0.001
        for trail in (trail_core, trail_glow):
            trail.location = position
            trail.scale = (size, size, length)
            trail.keyframe_insert("location", frame=frame)
            trail.keyframe_insert("scale", frame=frame)
        light.location = position
        landing = progress(seconds, METEOR_END - 0.5, METEOR_END + 0.35)
        light_data.energy = (4e6 + 4e7 * landing ** 2) * tier["light"] * flight if visible else 0
        light_data.keyframe_insert("energy", frame=frame)
        ground = Vector((position.x, position.y, 0.3))
        base = Vector((final_location.x, final_location.y, 0.3))
        reach = (ground - base).length
        glint.location = base
        glint.rotation_euler = (0, 0, math.atan2(-(ground - base).x, (ground - base).y))
        glint.scale = (reach, reach, 1)
        glint.keyframe_insert("location", frame=frame)
        glint.keyframe_insert("rotation_euler", frame=frame)
        glint.keyframe_insert("scale", frame=frame)
        key_value(glint_strength, frame, (0.6 + 5 * landing ** 2) * min(1.0, tier["light"] / 2) * flight if visible else 0)

    # Sparks shed along the fall, each drifting away while it fades.
    rng = random.Random(seed)
    spark_material = glow_material("Spark", palette["core"], 30, falloff=False)
    for index in range(tier["sparks"]):
        born = METEOR_START + rng.random() * (METEOR_END - METEOR_START) * 0.97
        life = 0.25 + rng.random() * 0.3
        drift = Vector((rng.uniform(-1, 1), rng.uniform(-0.6, 0.6), rng.uniform(-1, 0.4))) * 70
        origin = start.lerp(impact, ease_in_quad(progress(born, METEOR_START, METEOR_END)))
        spark = link(scene, no_shadow(uv_sphere(f"Spark{index}", rng.uniform(0.8, 2.2) * tier["head"], 8, 4)))
        spark.data.materials.append(spark_material)
        for frame in range(max(1, frame_at(born) - 1), min(CUT_FRAME, frame_at(born + life) + 2)):
            age = (seconds_at(frame) - born) / life
            alive = 0 <= age <= 1
            spark.location = origin + drift * max(0.0, age) + Vector((0, 0, -25)) * max(0.0, age) ** 2
            size = (1 - age) if alive else 0.0001
            spark.scale = (size, size, size)
            spark.keyframe_insert("location", frame=frame)
            spark.keyframe_insert("scale", frame=frame)
    return scene


# --- reveal scene ---------------------------------------------------------

ORTHO_WIDTH = 16.0


def screen_to_ortho(sx, sy):
    ortho_height = ORTHO_WIDTH * HEIGHT / WIDTH
    return Vector(((sx - 0.5) * ORTHO_WIDTH, 0, (0.5 - sy) * ortho_height))


def backdrop_material(palette, center):
    """Radial glow of the rarity colour behind the emblem, plus light rays."""
    material, nodes, links = node_material("Backdrop")
    output = nodes.new("ShaderNodeOutputMaterial")
    emission = nodes.new("ShaderNodeEmission")
    coords = nodes.new("ShaderNodeTexCoord")
    offset = nodes.new("ShaderNodeVectorMath")
    offset.operation = "SUBTRACT"
    offset.inputs[1].default_value = center
    links.new(coords.outputs["Object"], offset.inputs[0])
    split = nodes.new("ShaderNodeSeparateXYZ")
    links.new(offset.outputs[0], split.inputs[0])
    radius = nodes.new("ShaderNodeVectorMath")
    radius.operation = "LENGTH"
    links.new(offset.outputs[0], radius.inputs[0])
    glow = nodes.new("ShaderNodeValToRGB")
    glow.color_ramp.elements[0].color = (*[c * 2.2 for c in palette["deep"]], 1)
    glow.color_ramp.elements[1].position = 1.0
    glow.color_ramp.elements[1].color = (0.004, 0.003, 0.012, 1)
    mid = glow.color_ramp.elements.new(0.45)
    mid.color = (*[c * 0.5 for c in palette["deep"]], 1)
    scale_radius = nodes.new("ShaderNodeMath")
    scale_radius.operation = "DIVIDE"
    scale_radius.inputs[1].default_value = 11
    links.new(radius.outputs["Value"], scale_radius.inputs[0])
    links.new(scale_radius.outputs[0], glow.inputs["Fac"])
    angle = nodes.new("ShaderNodeMath")
    angle.operation = "ARCTAN2"
    links.new(split.outputs["Z"], angle.inputs[0])
    links.new(split.outputs["X"], angle.inputs[1])
    fade = nodes.new("ShaderNodeMapRange")
    fade.inputs["From Min"].default_value = 0.5
    fade.inputs["From Max"].default_value = 7
    fade.inputs["To Min"].default_value = 1
    fade.inputs["To Max"].default_value = 0
    links.new(radius.outputs["Value"], fade.inputs["Value"])
    material["ray_layers"] = []
    return material, nodes, links, emission, output, glow, angle, fade


def add_ray_layer(nodes, links, angle, fade, count, sharpness, spin_socket_name):
    turn = nodes.new("ShaderNodeValue")
    turn.name = spin_socket_name
    phase = nodes.new("ShaderNodeMath")
    phase.operation = "MULTIPLY"
    phase.inputs[1].default_value = count / 2
    links.new(angle.outputs[0], phase.inputs[0])
    shifted = nodes.new("ShaderNodeMath")
    shifted.operation = "ADD"
    links.new(phase.outputs[0], shifted.inputs[0])
    links.new(turn.outputs[0], shifted.inputs[1])
    wave = nodes.new("ShaderNodeMath")
    wave.operation = "SINE"
    links.new(shifted.outputs[0], wave.inputs[0])
    absolute = nodes.new("ShaderNodeMath")
    absolute.operation = "ABSOLUTE"
    links.new(wave.outputs[0], absolute.inputs[0])
    sharp = nodes.new("ShaderNodeMath")
    sharp.operation = "POWER"
    sharp.inputs[1].default_value = sharpness
    links.new(absolute.outputs[0], sharp.inputs[0])
    faded = nodes.new("ShaderNodeMath")
    faded.operation = "MULTIPLY"
    links.new(sharp.outputs[0], faded.inputs[0])
    links.new(fade.outputs[0], faded.inputs[1])
    return turn, faded


def emblem_material(name, base, emission_color, metallic, roughness):
    material, nodes, links = node_material(name)
    output = nodes.new("ShaderNodeOutputMaterial")
    bsdf = nodes.new("ShaderNodeBsdfPrincipled")
    bsdf.inputs["Base Color"].default_value = (*base, 1)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    bsdf.inputs["Coat Weight"].default_value = 0.4
    bsdf.inputs["Coat Roughness"].default_value = 0.05
    bsdf.inputs["Emission Color"].default_value = (*emission_color, 1)
    # The emblem starts as a white-hot silhouette and cools into its colours.
    white = nodes.new("ShaderNodeEmission")
    white.inputs["Color"].default_value = (1, 1, 1, 1)
    white.inputs["Strength"].default_value = 12
    mix = nodes.new("ShaderNodeMixShader")
    mix.name = "Silhouette"
    links.new(bsdf.outputs[0], mix.inputs[1])
    links.new(white.outputs[0], mix.inputs[2])
    links.new(mix.outputs[0], output.inputs["Surface"])
    return material, bsdf


def diamond(name, size, depth, bevel):
    verts = [(0, -depth, size), (size, -depth, 0), (0, -depth, -size), (-size, -depth, 0),
             (0, depth, size), (size, depth, 0), (0, depth, -size), (-size, depth, 0)]
    faces = [(0, 3, 2, 1), (4, 5, 6, 7), (0, 1, 5, 4), (1, 2, 6, 5), (2, 3, 7, 6), (3, 0, 4, 7)]
    obj = make_mesh(name, verts, faces)
    modifier = obj.modifiers.new("Bevel", "BEVEL")
    modifier.width = bevel
    modifier.segments = 4
    modifier.limit_method = "NONE"
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    return obj


def four_point_star(name, size):
    verts = [(0, 0, 0)]
    for index in range(8):
        angle = math.pi / 4 * index
        reach = size if index % 2 == 0 else size * 0.28
        verts.append((math.cos(angle) * reach, 0, math.sin(angle) * reach))
    faces = [(0, 1 + index, 1 + (index + 1) % 8) for index in range(8)]
    return make_mesh(name, verts, faces)


def build_reveal_scene(rarity, seed):
    palette, tier = PALETTES[rarity], TIERS[rarity]
    scene = new_scene("Reveal", CUT_FRAME, LAST_FRAME)
    world = bpy.data.worlds.new(f"{PREFIX}RevealWorld")
    # A faint rarity tint gives the metal frame something to reflect.
    world.color = tuple(c * 0.6 for c in palette["deep"])
    scene.world = world
    setup_compositor(scene, "Reveal", False)

    camera_data = bpy.data.cameras.new(f"{PREFIX}RevealCamera")
    camera_data.type = "ORTHO"
    camera_data.ortho_scale = ORTHO_WIDTH
    camera_data.sensor_fit = "HORIZONTAL"
    camera = link(scene, bpy.data.objects.new(f"{PREFIX}RevealCamera", camera_data))
    camera.location = (0, -20, 0)
    camera.rotation_euler = (math.radians(90), 0, 0)
    scene.camera = camera

    center = screen_to_ortho(*EMBLEM_SCREEN)
    reveal_frames = range(CUT_FRAME, LAST_FRAME + 1)

    # Backdrop with the rotating light rays.
    material, nodes, links, emission, output, glow, angle, fade = backdrop_material(palette, center)
    layers = [add_ray_layer(nodes, links, angle, fade, 18, 6, "SpinA")]
    if tier["counter"]:
        layers.append(add_ray_layer(nodes, links, angle, fade, 26, 14, "SpinB"))
    total = layers[0][1]
    for _, extra in layers[1:]:
        combined = nodes.new("ShaderNodeMath")
        combined.operation = "ADD"
        links.new(total.outputs[0], combined.inputs[0])
        links.new(extra.outputs[0], combined.inputs[1])
        total = combined
    strength = nodes.new("ShaderNodeMath")
    strength.operation = "MULTIPLY"
    strength.name = "RayStrength"
    links.new(total.outputs[0], strength.inputs[0])
    _, rays_a, rays_b, rays_result = mix_color(nodes, "ADD")
    links.new(glow.outputs["Color"], rays_a)
    _, tint_a, tint_b, tint_result = mix_color(nodes, "MULTIPLY")
    tint_a.default_value = (*palette["glow"], 1)
    links.new(strength.outputs[0], tint_b)
    links.new(tint_result, rays_b)
    links.new(rays_result, emission.inputs["Color"])
    emission.inputs["Strength"].default_value = 1
    links.new(emission.outputs[0], output.inputs["Surface"])
    backdrop = link(scene, make_mesh("Backdrop", [(-20, 0, -12), (20, 0, -12), (20, 0, 12), (-20, 0, 12)], [(0, 1, 2, 3)]))
    backdrop.location = (0, 10, 0)
    backdrop.data.materials.append(material)

    for frame in reveal_frames:
        seconds = seconds_at(frame)
        swell = ease_out_cubic(progress(seconds, REVEAL_START, EMBLEM_SETTLED))
        key_value(nodes["RayStrength"].inputs[1], frame, swell * tier["rays"])
        key_value(nodes["SpinA"].outputs[0], frame, seconds * 0.16)
        if tier["counter"]:
            key_value(nodes["SpinB"].outputs[0], frame, -seconds * 0.3)

    # The emblem: a bevelled diamond frame, an inset face, and the rank letter.
    rig = link(scene, bpy.data.objects.new(f"{PREFIX}EmblemRig", None))
    rig.location = center
    frame_metal = (1.0, 0.86, 0.52) if rarity == 5 else (0.92, 0.94, 1.0)
    outer_material, _ = emblem_material("EmblemFrame", frame_metal, palette["glow"], 1.0, 0.18)
    face_material, face_bsdf = emblem_material("EmblemFace", palette["face"], palette["face"], 0.0, 0.35)
    # Matte and dim: brighter faces wash out to pastel under the AgX view.
    face_bsdf.inputs["Emission Strength"].default_value = 0.12
    face_bsdf.inputs["Coat Weight"].default_value = 0
    face_bsdf.inputs["Specular IOR Level"].default_value = 0.1
    letter_material, letter_bsdf = emblem_material(
        "EmblemLetter", (1.0, 0.95, 0.75) if rarity == 5 else (1, 1, 1), palette["core"], 0.7, 0.15)
    letter_bsdf.inputs["Emission Strength"].default_value = 2.5

    emblem_size = 2.35
    outer = link(scene, diamond("EmblemFrame", emblem_size, 0.3, 0.12))
    outer.data.materials.append(outer_material)
    face = link(scene, diamond("EmblemFace", emblem_size * 0.84, 0.36, 0.08))
    face.data.materials.append(face_material)
    for part in (outer, face):
        part.parent = rig

    curve = bpy.data.curves.new(f"{PREFIX}RankLetter", "FONT")
    curve.body = {3: "B", 4: "A", 5: "S"}[rarity]
    for path in FONT_PATHS:
        try:
            curve.font = bpy.data.fonts.load(path, check_existing=True)
            break
        except RuntimeError:
            continue
    curve.size = 2.4
    curve.shear = 0.18
    curve.extrude = 0.14
    curve.bevel_depth = 0.035
    curve.align_x = "CENTER"
    curve.align_y = "CENTER"
    letter = link(scene, bpy.data.objects.new(f"{PREFIX}RankLetter", curve))
    letter.data.materials.append(letter_material)
    letter.rotation_euler = (math.radians(90), 0, 0)
    letter.location = (-0.05, -0.55, 0)
    letter.parent = rig

    for frame in reveal_frames:
        seconds = seconds_at(frame)
        pop = progress(seconds, REVEAL_START, REVEAL_START + 0.45)
        settle = progress(seconds, REVEAL_START + 0.45, EMBLEM_SETTLED)
        overshoot = 1 + 0.04 * tier["head"]
        scale = 0.72 + (overshoot - 0.72) * ease_out_cubic(pop) if pop < 1 else overshoot + (1 - overshoot) * ease_out_cubic(settle)
        turn = ease_out_cubic(progress(seconds, REVEAL_START, EMBLEM_SETTLED + 0.2))
        bob = 0.06 * math.sin((seconds - REVEAL_START) * 2.2)
        rig.scale = (scale, scale, scale)
        rig.rotation_euler = (0, 0, math.radians(-75 * (1 - turn) + 8 * math.sin((seconds - REVEAL_START) * 1.3) * turn))
        rig.location = center + Vector((0, 0, bob * turn))
        rig.keyframe_insert("scale", frame=frame)
        rig.keyframe_insert("rotation_euler", frame=frame)
        rig.keyframe_insert("location", frame=frame)
        silhouette = 1 - progress(seconds, REVEAL_START + 0.2, EMBLEM_SETTLED)
        for item in (outer_material, face_material, letter_material):
            key_value(item.node_tree.nodes["Silhouette"].inputs["Fac"], frame, silhouette)

    # Lighting: key from the upper left, a rarity-coloured rim, and for 5★ a
    # light that sweeps across the emblem.
    def area(name, location, energy, color, size, target):
        data = bpy.data.lights.new(f"{PREFIX}{name}", "AREA")
        data.energy = energy
        data.color = color
        data.size = size
        obj = link(scene, bpy.data.objects.new(f"{PREFIX}{name}", data))
        obj.location = location
        obj.rotation_euler = (target - Vector(location)).to_track_quat("-Z", "Y").to_euler()
        return obj

    area("KeyLight", center + Vector((-5, -8, 5)), 120, (1, 0.97, 0.92), 4, center)
    area("RimLight", center + Vector((6, -2, -3)), 250, palette["glow"], 3, center)
    if tier["sweep"]:
        sweep = area("SweepLight", center + Vector((-8, -6, 2)), 900, (1, 0.95, 0.8), 1.2, center)
        for frame in reveal_frames:
            seconds = seconds_at(frame)
            phase = ((seconds - EMBLEM_SETTLED) / 1.8) % 1 if seconds >= EMBLEM_SETTLED else 0
            sweep.location = center + Vector((-8 + 16 * phase, -6, 2 - 4 * phase))
            sweep.rotation_euler = (center - sweep.location).to_track_quat("-Z", "Y").to_euler()
            sweep.keyframe_insert("location", frame=frame)
            sweep.keyframe_insert("rotation_euler", frame=frame)

    # Shockwave rings bursting from the emblem.
    for index in range(tier["rings"]):
        ring_material = glow_material(f"Ring{index}", palette["glow"] if index == 0 else palette["core"], 10, falloff=False)
        ring_mesh = bpy.data.meshes.new(f"{PREFIX}Ring{index}")
        segments = 96
        inner, outer_radius = 1.0, 1.0 + 0.07 - index * 0.015
        verts = [(math.cos(2 * math.pi * s / segments) * r, 0, math.sin(2 * math.pi * s / segments) * r)
                 for r in (inner, outer_radius) for s in range(segments)]
        faces = [(s, (s + 1) % segments, segments + (s + 1) % segments, segments + s) for s in range(segments)]
        ring_mesh.from_pydata(verts, [], faces)
        ring = link(scene, no_shadow(bpy.data.objects.new(f"{PREFIX}Ring{index}", ring_mesh)))
        ring.data.materials.append(ring_material)
        ring.location = center + Vector((0, 1, 0))
        start = REVEAL_START + index * 0.16
        for frame in reveal_frames:
            spread = ease_out_cubic(progress(seconds_at(frame), start, start + 0.9))
            alive = 0 < spread < 1
            size = (0.9 + spread * (5.5 + index * 1.8)) if alive else 0.0001
            ring.scale = (size, size, size)
            ring.keyframe_insert("scale", frame=frame)
            key_value(strength_socket(ring_material), frame, 10 * (1 - spread) if alive else 0)

    # Motes rising behind the emblem and, for 5★, sparkles raining down.
    rng = random.Random(seed + 2)
    ortho_height = ORTHO_WIDTH * HEIGHT / WIDTH
    mote_material = glow_material("Mote", palette["core"], 8)
    duration = TOTAL_SECONDS - REVEAL_START
    for index in range(tier["motes"]):
        mote = link(scene, no_shadow(uv_sphere(f"Mote{index}", rng.uniform(0.03, 0.08), 10, 6)))
        mote.data.materials.append(mote_material)
        x = rng.uniform(-ORTHO_WIDTH / 2, ORTHO_WIDTH / 2)
        z0 = rng.uniform(-ortho_height * 0.9, ortho_height * 0.4)
        rise = rng.uniform(0.4, 1.0)
        for frame, t in ((CUT_FRAME, 0.0), (LAST_FRAME, duration)):
            mote.location = (x + 0.15 * math.sin(t + index), 2, z0 + rise * t)
            mote.keyframe_insert("location", frame=frame)
    rain_material = glow_material("Rain", palette["core"], 9, falloff=False)
    for index in range(tier["rain"]):
        sparkle = link(scene, no_shadow(four_point_star(f"Rain{index}", rng.uniform(0.09, 0.17))))
        sparkle.data.materials.append(rain_material)
        x = rng.uniform(-ORTHO_WIDTH / 2, ORTHO_WIDTH / 2)
        z0 = rng.uniform(ortho_height * 0.1, ortho_height * 1.3)
        fall = rng.uniform(1.0, 2.0)
        spin = rng.uniform(-3, 3)
        for frame, t in ((CUT_FRAME, 0.0), (LAST_FRAME, duration)):
            sparkle.location = (x, 1.5, z0 - fall * t)
            sparkle.rotation_euler = (0, spin * t, 0)
            sparkle.keyframe_insert("location", frame=frame)
            sparkle.keyframe_insert("rotation_euler", frame=frame)

    for obj in scene.objects:
        if obj.animation_data and obj.animation_data.action:
            set_linear(obj.animation_data.action)
    return scene


def set_linear(action):
    """Linear keys: per-frame keys need no easing, and two-key drifts move evenly."""
    fcurves = getattr(action, "fcurves", None)
    if fcurves is None:
        fcurves = [c for layer in action.layers for strip in layer.strips for bag in strip.channelbags for c in bag.fcurves]
    for curve in fcurves:
        for point in curve.keyframe_points:
            point.interpolation = "LINEAR"


def build(rarity, seed=7):
    remove_previous()
    meteor = build_meteor_scene(rarity, seed)
    for obj in meteor.objects:
        if obj.animation_data and obj.animation_data.action:
            set_linear(obj.animation_data.action)
    reveal = build_reveal_scene(rarity, seed)
    return meteor, reveal


def render(scenes, out_dir):
    for scene in scenes:
        scene.render.filepath = f"{out_dir.rstrip('/')}/frame-####"
        bpy.ops.render.render(animation=True, scene=scene.name)


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    parser = argparse.ArgumentParser(description="Render the /genshin wish backdrop frames.")
    parser.add_argument("--rarity", type=int, choices=(3, 4, 5), required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--seed", type=int, default=7)
    args = parser.parse_args(argv)
    render(build(args.rarity, args.seed), args.out)


if __name__ == "__main__" and bpy.app.background:
    main()
