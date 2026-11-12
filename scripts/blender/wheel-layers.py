"""Renders the static layers of the /wheelofnames wheel.

The bot draws the spinning disc (segments, labels, pegs) itself; Blender
supplies everything that does not depend on the options, rendered straight on
from above with an orthographic camera so the layers line up pixel for pixel:

- backdrop.png  the stage and the wheel's back plate (opaque)
- front.png     the rim, bulb sockets, and hub, over the disc (alpha)
- shade.png     diffuse light on a white disc, with the rim's, hub's, and
                pointer's shadows (grey; the bot multiplies the segments by it)
- spec.png      the disc's clear-coat highlights (the bot adds them)
- pointer.png   the pointer alone (alpha); it pivots at POINTER_PIVOT
- peg.png       one peg seen from above (alpha), PEG_SPRITE pixels square

Run it through `scripts/gen-assets.sh wheel`. On its own (Blender 4.2 or
newer, Eevee):

    blender -b --factory-startup -P scripts/blender/wheel-layers.py -- \\
        --out internal/modules/random/wheel/assets

Geometry is in Blender units of 100 pixels and must match the constants in
internal/modules/random/wheel/layout.go.
"""

import argparse
import math
import os
import sys

import bpy
from mathutils import Vector

SIZE = 512                 # output pixels, square
UNIT = 100                 # pixels per Blender unit
DISC_RADIUS = 2.0          # 200 px
RIM_RADIUS = 2.09
RIM_TUBE = 0.13
BULBS = 24
HUB_RADIUS = 0.3
POINTER_PIVOT = (0.0, 2.3)  # the pointer turns about this point
POINTER_TIP = 1.84          # the tip reaches down to this radius
PEG_SPRITE = 36            # px
PEG_RADIUS = 0.07

PREFIX = "Wheel"


def remove_previous():
    for scene in [s for s in bpy.data.scenes if s.name.startswith(PREFIX)]:
        for obj in list(scene.objects):
            bpy.data.objects.remove(obj, do_unlink=True)
        bpy.data.scenes.remove(scene)
    for collection in (bpy.data.meshes, bpy.data.materials, bpy.data.lights, bpy.data.cameras,
                       bpy.data.worlds, bpy.data.curves):
        for block in [b for b in collection if b.name.startswith(PREFIX) and b.users == 0]:
            collection.remove(block)


def new_scene(name, size):
    scene = bpy.data.scenes.new(f"{PREFIX}{name}")
    scene.render.engine = "BLENDER_EEVEE"
    scene.render.resolution_x = size
    scene.render.resolution_y = size
    scene.render.resolution_percentage = 100
    scene.render.image_settings.file_format = "PNG"
    scene.render.image_settings.color_depth = "8"
    scene.eevee.taa_render_samples = 64
    scene.eevee.use_raytracing = True
    scene.view_settings.view_transform = "Standard"
    scene.view_settings.look = "None"
    world = bpy.data.worlds.new(f"{PREFIX}{name}World")
    world.color = (0.02, 0.02, 0.03)
    scene.world = world
    return scene


def link(scene, obj):
    scene.collection.objects.link(obj)
    return obj


def material(name, base, metallic=0.0, roughness=0.5, coat=0.0, emission=None, strength=0.0):
    mat = bpy.data.materials.new(f"{PREFIX}{name}")
    if mat.node_tree is None:
        mat.use_nodes = True
    bsdf = mat.node_tree.nodes.get("Principled BSDF")
    bsdf.inputs["Base Color"].default_value = (*base, 1)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    bsdf.inputs["Coat Weight"].default_value = coat
    bsdf.inputs["Coat Roughness"].default_value = 0.08
    if emission is not None:
        bsdf.inputs["Emission Color"].default_value = (*emission, 1)
        bsdf.inputs["Emission Strength"].default_value = strength
    return mat


def mesh_object(scene, name, verts, faces, mat=None, smooth=True):
    mesh = bpy.data.meshes.new(f"{PREFIX}{name}")
    mesh.from_pydata(verts, [], faces)
    mesh.update()
    if smooth:
        for polygon in mesh.polygons:
            polygon.use_smooth = True
    obj = link(scene, bpy.data.objects.new(f"{PREFIX}{name}", mesh))
    if mat is not None:
        obj.data.materials.append(mat)
    return obj


def cylinder(scene, name, radius, z0, z1, mat, segments=128, cap=True):
    verts = []
    for z in (z0, z1):
        for i in range(segments):
            a = 2 * math.pi * i / segments
            verts.append((radius * math.cos(a), radius * math.sin(a), z))
    faces = [(i, (i + 1) % segments, segments + (i + 1) % segments, segments + i) for i in range(segments)]
    if cap:
        faces.append(tuple(range(segments - 1, -1, -1)))
        faces.append(tuple(range(segments, 2 * segments)))
    obj = mesh_object(scene, name, verts, faces, mat)
    bevel = obj.modifiers.new("Bevel", "BEVEL")
    bevel.width = min(0.02, (z1 - z0) / 3)
    bevel.segments = 3
    bevel.limit_method = "ANGLE"
    obj.data.polygons.foreach_set("use_smooth", [False] * len(obj.data.polygons))
    return obj


def torus(scene, name, major, minor, z, mat, segments=192, rings=24):
    verts = []
    for i in range(segments):
        a = 2 * math.pi * i / segments
        for j in range(rings):
            b = 2 * math.pi * j / rings
            r = major + minor * math.cos(b)
            verts.append((r * math.cos(a), r * math.sin(a), z + minor * math.sin(b)))
    faces = []
    for i in range(segments):
        for j in range(rings):
            a, b = i * rings + j, i * rings + (j + 1) % rings
            c, d = ((i + 1) % segments) * rings + (j + 1) % rings, ((i + 1) % segments) * rings + j
            faces.append((a, d, c, b))
    return mesh_object(scene, name, verts, faces, mat)


def sphere(scene, name, radius, location, mat, flatten=1.0, segments=24, rings=12):
    verts = [(0, 0, radius * flatten)]
    for ring in range(1, rings):
        phi = math.pi * ring / rings
        for seg in range(segments):
            theta = 2 * math.pi * seg / segments
            verts.append((radius * math.sin(phi) * math.cos(theta), radius * math.sin(phi) * math.sin(theta),
                          radius * math.cos(phi) * flatten))
    verts.append((0, 0, -radius * flatten))
    faces = [(0, 1 + (s + 1) % segments, 1 + s) for s in range(segments)]
    for ring in range(rings - 2):
        a, b = 1 + ring * segments, 1 + (ring + 1) * segments
        faces += [(a + s, a + (s + 1) % segments, b + (s + 1) % segments, b + s) for s in range(segments)]
    last = len(verts) - 1
    base = 1 + (rings - 2) * segments
    faces += [(base + s, base + (s + 1) % segments, last) for s in range(segments)]
    obj = mesh_object(scene, name, verts, faces, mat)
    obj.location = location
    return obj


def pointer_mesh(scene, mat, pin_mat):
    """A tapered flapper hanging from the pivot, its tip pointing at the disc."""
    px, py = POINTER_PIVOT
    outline = []
    head = 0.17
    for i in range(25):
        a = math.pi * (-0.15 + 1.3 * i / 24)
        outline.append((px + head * math.cos(a), py + head * math.sin(a)))
    outline += [(px - 0.12, py - 0.2), (px - 0.035, POINTER_TIP + 0.06), (px, POINTER_TIP),
                (px + 0.035, POINTER_TIP + 0.06), (px + 0.12, py - 0.2)]
    n = len(outline)
    z0, z1 = 0.2, 0.27
    verts = [(x, y, z0) for x, y in outline] + [(x, y, z1) for x, y in outline]
    faces = [tuple(range(n - 1, -1, -1)), tuple(range(n, 2 * n))]
    faces += [(i, (i + 1) % n, n + (i + 1) % n, n + i) for i in range(n)]
    obj = mesh_object(scene, "Pointer", verts, faces, mat, smooth=False)
    bevel = obj.modifiers.new("Bevel", "BEVEL")
    bevel.width = 0.025
    bevel.segments = 4
    obj.modifiers.new("Smooth", "WEIGHTED_NORMAL")
    pin = cylinder(scene, "PointerPin", 0.06, 0.2, 0.31, pin_mat, segments=48)
    pin.location = (px, py, 0)
    cap = sphere(scene, "PointerCap", 0.06, (px, py, 0.31), pin_mat, flatten=0.5)
    return [obj, pin, cap]


def halo_material():
    """A warm glow on the stage behind the wheel, fading out from its edge."""
    mat = bpy.data.materials.new(f"{PREFIX}Halo")
    if mat.node_tree is None:
        mat.use_nodes = True
    nodes, links = mat.node_tree.nodes, mat.node_tree.links
    nodes.clear()
    output = nodes.new("ShaderNodeOutputMaterial")
    add = nodes.new("ShaderNodeAddShader")
    clear = nodes.new("ShaderNodeBsdfTransparent")
    glow = nodes.new("ShaderNodeEmission")
    coords = nodes.new("ShaderNodeTexCoord")
    length = nodes.new("ShaderNodeVectorMath")
    length.operation = "LENGTH"
    links.new(coords.outputs["Object"], length.inputs[0])
    ramp = nodes.new("ShaderNodeValToRGB")
    ramp.color_ramp.elements[0].position = 2.1 / 3.4
    ramp.color_ramp.elements[0].color = (0.75, 0.3, 0.7, 1)
    ramp.color_ramp.elements[1].position = 1.0
    ramp.color_ramp.elements[1].color = (0, 0, 0, 1)
    scale = nodes.new("ShaderNodeMath")
    scale.operation = "DIVIDE"
    scale.inputs[1].default_value = 3.4
    links.new(length.outputs["Value"], scale.inputs[0])
    links.new(scale.outputs[0], ramp.inputs["Fac"])
    links.new(ramp.outputs["Color"], glow.inputs["Color"])
    glow.inputs["Strength"].default_value = 0.22
    links.new(clear.outputs[0], add.inputs[0])
    links.new(glow.outputs[0], add.inputs[1])
    links.new(add.outputs[0], output.inputs["Surface"])
    mat.surface_render_method = "BLENDED"
    return mat


def build_wheel(scene):
    gold = material("Gold", (1.0, 0.72, 0.32), metallic=1.0, roughness=0.22)
    dark_gold = material("DarkGold", (0.55, 0.36, 0.12), metallic=1.0, roughness=0.35)
    plate = material("Plate", (0.05, 0.03, 0.025), metallic=0.2, roughness=0.45, coat=0.6)
    bulb = material("Bulb", (1.0, 0.9, 0.7), roughness=0.15, coat=1.0, emission=(1.0, 0.85, 0.6), strength=0.4)
    red = material("PointerRed", (0.75, 0.04, 0.03), roughness=0.25, coat=1.0)
    stage = material("Stage", (0.05, 0.035, 0.09), roughness=0.8)

    parts = {}
    backdrop = mesh_object(scene, "Backdrop", [(-8, -8, -0.6), (8, -8, -0.6), (8, 8, -0.6), (-8, 8, -0.6)], [(0, 1, 2, 3)], stage, smooth=False)
    halo = mesh_object(scene, "Halo", [(-3.4, -3.4, -0.55), (3.4, -3.4, -0.55), (3.4, 3.4, -0.55), (-3.4, 3.4, -0.55)], [(0, 1, 2, 3)], halo_material(), smooth=False)
    halo.visible_shadow = False
    parts["backdrop"] = [backdrop, halo]
    parts["plate"] = [cylinder(scene, "BackPlate", 2.26, -0.25, 0.0, plate)]
    disc = cylinder(scene, "Disc", DISC_RADIUS, 0.0, 0.06, None)
    parts["disc"] = [disc]
    rim = torus(scene, "Rim", RIM_RADIUS, RIM_TUBE, 0.1, gold)
    lip = torus(scene, "RimLip", DISC_RADIUS + 0.01, 0.025, 0.07, dark_gold, rings=12)
    bulbs = []
    for i in range(BULBS):
        a = math.pi / 2 - 2 * math.pi * i / BULBS
        x, y = RIM_RADIUS * math.cos(a), RIM_RADIUS * math.sin(a)
        socket = cylinder(scene, f"Socket{i}", 0.075, 0.15, 0.21, dark_gold, segments=32)
        socket.location = (x, y, 0)
        bulbs += [socket, sphere(scene, f"Bulb{i}", 0.065, (x, y, 0.23), bulb)]
    hub = cylinder(scene, "Hub", HUB_RADIUS, 0.06, 0.13, gold)
    dome = sphere(scene, "HubDome", HUB_RADIUS * 0.72, (0, 0, 0.13), gold, flatten=0.45)
    ring = torus(scene, "HubRing", HUB_RADIUS * 0.86, 0.02, 0.14, dark_gold, segments=96, rings=12)
    parts["front"] = [rim, lip, hub, dome, ring] + bulbs
    parts["pointer"] = pointer_mesh(scene, red, gold)

    def area(name, location, energy, size, color=(1, 1, 1)):
        data = bpy.data.lights.new(f"{PREFIX}{name}", "AREA")
        data.energy = energy
        data.size = size
        data.color = color
        obj = link(scene, bpy.data.objects.new(f"{PREFIX}{name}", data))
        obj.location = location
        obj.rotation_euler = (Vector((0, 0, 0)) - Vector(location)).to_track_quat("-Z", "Y").to_euler()
        return obj

    area("Key", (-3.2, 3.6, 7.0), 330, 2.5, (1.0, 0.96, 0.9))
    area("Fill", (4.5, -2.5, 6.0), 90, 4.0, (0.85, 0.9, 1.0))
    # Straight above the upper left of the disc, so the camera sees its
    # reflection there as the clear coat's highlight.
    highlight = area("Highlight", (-1.0, 1.15, 8.0), 110, 1.3)
    parts["highlight"] = [highlight]

    camera_data = bpy.data.cameras.new(f"{PREFIX}Camera")
    camera_data.type = "ORTHO"
    camera_data.ortho_scale = SIZE / UNIT
    camera = link(scene, bpy.data.objects.new(f"{PREFIX}Camera", camera_data))
    camera.location = (0, 0, 10)
    scene.camera = camera
    return parts


def show_only(parts, visible):
    """Shows `visible` groups to the camera; every object still casts light
    and shadow. The highlight light shines only for the clear-coat pass."""
    for group, objects in parts.items():
        for obj in objects:
            if group == "highlight":
                obj.hide_render = "highlight" not in visible
                continue
            obj.visible_camera = group in visible
            obj.hide_render = False


def disc_material(disc, mat):
    disc.data.materials.clear()
    disc.data.materials.append(mat)


def render(scene, path, transparent):
    scene.render.film_transparent = transparent
    scene.render.image_settings.color_mode = "RGBA" if transparent else "RGB"
    scene.render.filepath = path
    bpy.ops.render.render(write_still=True, scene=scene.name)


def build_and_render(out):
    remove_previous()
    os.makedirs(out, exist_ok=True)
    scene = new_scene("Layers", SIZE)
    parts = build_wheel(scene)
    disc = parts["disc"][0]
    white = material("DiscWhite", (0.8, 0.8, 0.8), roughness=1.0)
    white.node_tree.nodes["Principled BSDF"].inputs["Specular IOR Level"].default_value = 0
    gloss = material("DiscGloss", (0, 0, 0), roughness=0.3, coat=1.0)
    gloss.node_tree.nodes["Principled BSDF"].inputs["Coat Roughness"].default_value = 0.2

    show_only(parts, {"backdrop", "plate"})
    render(scene, f"{out}/backdrop.png", False)
    show_only(parts, {"front"})
    render(scene, f"{out}/front.png", True)
    show_only(parts, {"pointer"})
    render(scene, f"{out}/pointer.png", True)
    show_only(parts, {"disc"})
    disc_material(disc, white)
    render(scene, f"{out}/shade.png", False)
    disc_material(disc, gloss)
    show_only(parts, {"disc", "highlight"})
    render(scene, f"{out}/spec.png", False)

    peg_scene = new_scene("Peg", PEG_SPRITE)
    gold = bpy.data.materials[f"{PREFIX}Gold"]
    cylinder(peg_scene, "Peg", PEG_RADIUS, 0.0, 0.12, gold, segments=48)
    sphere(peg_scene, "PegCap", PEG_RADIUS, (0, 0, 0.12), gold, flatten=0.6)
    data = bpy.data.lights.new(f"{PREFIX}PegKey", "AREA")
    data.energy = 30
    data.size = 1.0
    key = link(peg_scene, bpy.data.objects.new(f"{PREFIX}PegKey", data))
    key.location = (-1.2, 1.4, 2.6)
    key.rotation_euler = (Vector((0, 0, 0)) - key.location).to_track_quat("-Z", "Y").to_euler()
    camera_data = bpy.data.cameras.new(f"{PREFIX}PegCamera")
    camera_data.type = "ORTHO"
    camera_data.ortho_scale = PEG_SPRITE / UNIT
    camera = link(peg_scene, bpy.data.objects.new(f"{PREFIX}PegCamera", camera_data))
    camera.location = (0, 0, 5)
    peg_scene.camera = camera
    render(peg_scene, f"{out}/peg.png", True)


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    parser = argparse.ArgumentParser(description="Render the /wheelofnames static layers.")
    parser.add_argument("--out", required=True)
    build_and_render(parser.parse_args(argv).out)


if __name__ == "__main__" and bpy.app.background:
    main()
