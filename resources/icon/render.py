#!/usr/bin/env python
"""render.py — 从 icon.svg 生成应用中心与 WebUI 所需的全部 PNG。

用法（在仓库根目录）:
    python resources/icon/render.py [--chrome PATH]

渲染器优先使用 cairosvg；不可用时回退到本机 Chrome/Edge 的
headless 截图（与浏览器像素级一致）。产物:
    fnos/ICON.PNG                  64x64   应用中心
    fnos/ICON_256.PNG              256x256 应用中心大图
    fnos/app/ui/images/icon-64.png 64x64   WebUI/桌面入口
    fnos/app/ui/images/icon-256.png 256x256
"""
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SVG = Path(__file__).resolve().parent / "icon.svg"
OUTS = [
    (ROOT / "fnos/ICON.PNG", 64),
    (ROOT / "fnos/ICON_256.PNG", 256),
    (ROOT / "fnos/app/ui/images/icon-64.png", 64),
    (ROOT / "fnos/app/ui/images/icon-256.png", 256),
]

HTML = """<!doctype html><meta charset="utf-8">
<style>html,body{{margin:0;padding:0;background:transparent}}
svg{{display:block;width:{size}px;height:{size}px}}</style>
{svg}"""


def find_chrome(explicit):
    if explicit:
        return explicit
    for p in (
        r"C:\Program Files\Google\Chrome\Application\chrome.exe",
        r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
        r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
        "/usr/bin/google-chrome",
        "/usr/bin/chromium",
    ):
        if Path(p).exists():
            return p
    return None


def render_cairosvg(size):
    import cairosvg  # type: ignore

    return cairosvg.svg2png(bytestring=SVG.read_bytes(encoding="utf-8"),
                            output_width=size, output_height=size)


def render_chrome(chrome, size):
    import time

    html = tempfile.NamedTemporaryFile(
        "w", suffix=".html", delete=False, encoding="utf-8", dir=tempfile.gettempdir())
    html.write(HTML.format(size=size, svg=SVG.read_text(encoding="utf-8")))
    html.close()
    out = Path(html.name).with_suffix(".png")
    shot = str(out)
    if shot.startswith("\\\\"):
        shot = "UNC" + shot  # headless cannot write UNC paths
    # A dedicated user-data-dir is mandatory: without it the invocation
    # can be handed off to an already-running Chrome profile and the
    # screenshot never appears.
    profile = tempfile.mkdtemp(prefix="gsrm-icon-chrome-")
    for mode in ("--headless=new", "--headless"):
        r = subprocess.run([
            chrome, mode, "--disable-gpu", "--no-sandbox",
            f"--user-data-dir={profile}",
            "--hide-scrollbars", "--default-background-color=00000000",
            f"--screenshot={shot}", f"--window-size={size},{size}",
            "file:///" + Path(html.name).as_posix().lstrip("/"),
        ], capture_output=True, timeout=60)
        # Chrome may flush the file just after exit; poll briefly.
        for _ in range(50):
            if out.exists() and out.stat().st_size > 0:
                data = out.read_bytes()
                out.unlink(missing_ok=True)
                shutil.rmtree(profile, ignore_errors=True)
                Path(html.name).unlink(missing_ok=True)
                return data
            time.sleep(0.1)
    shutil.rmtree(profile, ignore_errors=True)
    Path(html.name).unlink(missing_ok=True)
    raise RuntimeError("chrome screenshot failed: " + r.stderr.decode(errors="replace")[:400])


def main():
    explicit = None
    if "--chrome" in sys.argv:
        explicit = sys.argv[sys.argv.index("--chrome") + 1]
    try:
        for path, size in OUTS:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(render_cairosvg(size))
            print(f"cairosvg -> {path} ({size}x{size})")
        return
    except Exception as e:  # noqa: BLE001
        print(f"cairosvg unavailable ({e}); falling back to headless Chrome")

    chrome = find_chrome(explicit)
    if not chrome:
        sys.exit("no renderer: install cairosvg or Chrome/Edge")
    for path, size in OUTS:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(render_chrome(chrome, size))
        print(f"chrome -> {path} ({size}x{size})")


if __name__ == "__main__":
    main()
