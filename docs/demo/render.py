"""Renders the demo clip frames from the tool output saved by capture.py."""
import json, os, shutil
from PIL import Image, ImageDraw, ImageFont

OUT = os.environ.get("DEMO_OUT", "/tmp/edge-mcp-demo")
D = json.load(open(os.path.join(OUT, "data.json")))
W, H = 1000, 600
PAD, TOP, LH = 28, 62, 24
FONT = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", 17)
BOLD = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf", 17)
CW = FONT.getlength("M")
BG, BAR, FG = (13, 17, 23), (22, 27, 34), (201, 209, 217)
DIM, GREEN, BLUE, YEL, RED, CYAN, PINK = (110, 118, 129), (63, 185, 80), (88, 166, 255), (210, 153, 34), (248, 81, 73), (86, 212, 221), (219, 97, 162)

frames_dir = os.path.join(OUT, "frames")
shutil.rmtree(frames_dir, ignore_errors=True); os.makedirs(frames_dir)
lines = [[]]          # each line: list of (text, color, bold)
n = 0

def draw(cursor=True):
    im = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(im)
    d.rectangle([0, 0, W, 40], fill=BAR)
    for i, c in enumerate([(255, 95, 86), (255, 189, 46), (39, 201, 63)]):
        d.ellipse([20 + i * 24, 14, 32 + i * 24, 26], fill=c)
    d.text((W / 2, 20), "edge-mcp --demo  ·  AI assistant", font=FONT, fill=DIM, anchor="mm")
    y = TOP
    for ln in lines:
        x = PAD
        for text, color, bold in ln:
            d.text((x, y), text, font=BOLD if bold else FONT, fill=color)
            x += CW * len(text)
        y += LH
    if cursor:
        x = PAD + CW * sum(len(t) for t, _, _ in lines[-1])
        d.rectangle([x, TOP + LH * (len(lines) - 1) + 2, x + CW * 0.6, TOP + LH * (len(lines) - 1) + LH - 3], fill=FG)
    return im

def emit(k=1, cursor=True):
    global n
    im = draw(cursor)
    for _ in range(k):
        im.save(f"{frames_dir}/f{n:05d}.png"); n += 1

def add(text, color=FG, bold=False): lines[-1].append((text, color, bold))
def nl(): lines.append([])
def type_(text, color=FG, bold=False, cps=3):
    for i in range(0, len(text), cps):
        add(text[i:i + cps], color, bold); emit()
def pause(k): emit(k)

# ---- real values from the captured tool output --------------------------------
ov = D["cluster_overview"]["result"]
bad = D["list_edge_nodes"]["result"]["nodes"][0]
wl = D["list_workloads"]["result"]["workloads"]
det = next(w for w in wl if w["name"] == "defect-detector")
log_last = D["get_pod_logs"]["result"]["log"].strip().splitlines()[-1]
model = log_last.split("Load model from ")[1].split(" failed")[0]

def tool(name, args, summary, color=GREEN):
    add("  ● ", BLUE); add(name, CYAN, True)
    if args: add(" " + args, DIM)
    emit(2)
    nl(); add("    ", FG)
    spin = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
    for i in range(9):   # spinner
        lines[-1] = [("    ", FG, False), (spin[i % 10] + " running…", DIM, False)]; emit()
    lines[-1] = [("    ", FG, False), ("✓ ", GREEN, True), (summary, FG, False)]; emit(8)
    nl()

# ---- script --------------------------------------------------------------------
pause(6)
add("you ", GREEN, True); add("› ", DIM)
type_("Which of my stores have problems right now?", FG, False, 2)
pause(8); nl(); nl()

tool("cluster_overview", "", f"{ov['nodes_ready']}/{ov['nodes']} nodes ready · needs attention: {', '.join(ov['nodes_needing_attention'])} · {ov['pods_failing']} pod failing")
tool("list_edge_nodes", "unhealthy_only=true", f"{bad['name']} ({bad['site']}) {bad['status']} · last heartbeat {bad['last_heartbeat']} ago", YEL)
tool("list_workloads", "unhealthy_only=true", f"{det['namespace']}/{det['name']} {det['ready']}/{det['desired']} ready · edge-system/model-runtime {wl[0]['ready']}/{wl[0]['desired']}")
tool("get_pod_logs", "pod=defect-detector-c previous=true", "NoSuchFile: " + model, RED)
nl(); pause(6)

add("assistant ", PINK, True); add("› ", DIM); nl()
answer = [
    (f"1 of {ov['nodes']} nodes needs attention: {bad['name']} at {bad['site']} has been NotReady", FG),
    (f"for {bad['last_heartbeat']} (no heartbeat). The site still has blr-01 serving.", FG),
    ("", FG),
    (f"Separately, {det['namespace']}/{det['name']} is degraded ({det['ready']}/{det['desired']}): the pod", FG),
    (f"defect-detector-c is crash-looping because {model} is missing.", FG),
    ("", FG),
    ("Want me to cordon blr-02 (dry run first)?", YEL),
]
for text, color in answer:
    add("  ", FG); type_(text, color, False, 4); nl()
pause(36)
print(n, "frames in", frames_dir)
