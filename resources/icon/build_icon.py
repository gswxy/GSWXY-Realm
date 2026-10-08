#!/usr/bin/env python
"""build_icon.py — 生成艾泽旅伴应用图标的 SVG 矢量源。

门框由连续双金环 + 角度均分的铆钉石 + 顶部拱心石构成（旋转对称，
任何尺寸下都整齐）。修改设计请改本脚本：

    python resources/icon/build_icon.py     # 生成 icon.svg
    python resources/icon/render.py         # 渲染全部 PNG
"""
import math
from pathlib import Path

CX, CY = 128.0, 132.0   # 传送门中心
RX, RY = 54.0, 68.0     # 门内虚空半径
STUDS = 12              # 铆钉石数量（含顶部拱心石位）


def ellipse_pt(rx: float, ry: float, deg: float) -> tuple[float, float]:
    t = math.radians(deg)
    return CX + rx * math.cos(t), CY + ry * math.sin(t)


def stud_svg(rx: float, ry: float, deg: float, r: float, fill: str) -> str:
    x, y = ellipse_pt(rx, ry, deg)
    return f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r}" fill="{fill}"/>'


def build_svg() -> str:
    mid_rx, mid_ry = RX + 10, RY + 10   # 铆钉石所在椭圆（两环中间）
    outer_rx, outer_ry = RX + 16, RY + 16

    studs = []
    for i in range(STUDS):
        deg = 360.0 * i / STUDS
        if i == 0:  # 顶部拱心石：稍大的菱形，浮在内环上方（留出间隙）
            x, y = ellipse_pt(mid_rx, mid_ry, 0)
            y -= 8  # 与内环上沿拉开距离，避免视觉粘连
            studs.append(
                f'<path d="M {x:.2f} {y-5:.2f} L {x+4.5:.2f} {y:.2f} '
                f'L {x:.2f} {y+5:.2f} L {x-4.5:.2f} {y:.2f} Z" fill="#e8c476"/>')
        else:
            studs.append(stud_svg(mid_rx, mid_ry, deg, 3.4, "#d9b05f"))

    studs_svg = "\n    ".join(studs)
    return f'''<?xml version="1.0" encoding="UTF-8"?>
<!--
  艾泽旅伴 · GSWXY Realm 应用图标（矢量源文件，由 build_icon.py 生成）
  设计：古老魔法传送门（双金环 + 铆钉石 + 拱心石）+ 旅伴之光（金色萤火）。
  深蓝 + 旧金配色。修改设计 → 编辑 build_icon.py → 运行它 → 运行 render.py。
-->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" role="img" aria-label="艾泽旅伴">
  <defs>
    <radialGradient id="void" cx="0.5" cy="0.44" r="0.78">
      <stop offset="0" stop-color="#2e5aa8"/>
      <stop offset="0.5" stop-color="#1d3866"/>
      <stop offset="1" stop-color="#0d1830"/>
    </radialGradient>
    <linearGradient id="goldEdge" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#dcb968"/>
      <stop offset="1" stop-color="#a67c2e"/>
    </linearGradient>
    <radialGradient id="glow" cx="0.5" cy="0.5" r="0.5">
      <stop offset="0" stop-color="#ffd977" stop-opacity="0.85"/>
      <stop offset="0.5" stop-color="#f2b93b" stop-opacity="0.25"/>
      <stop offset="1" stop-color="#f2b93b" stop-opacity="0"/>
    </radialGradient>
  </defs>

  <!-- 深蓝圆角底（浅色/深色桌面均可辨认） -->
  <rect x="10" y="10" width="236" height="236" rx="58" fill="#101d36"/>
  <rect x="10" y="10" width="236" height="236" rx="58" fill="none" stroke="#1b2c4e" stroke-width="2"/>

  <!-- 传送门虚空与门内冷色旋涡 -->
  <ellipse cx="{CX:.0f}" cy="{CY:.0f}" rx="{RX:.0f}" ry="{RY:.0f}" fill="url(#void)"/>
  <path d="M 102 170 C 86 145 98 106 {CX:.0f} 93 C 150 83 167 96 171 116
           C 163 104 146 98 133 106 C 113 116 106 141 117 164 Z"
        fill="#5b86d6" opacity="0.45"/>
  <path d="M 152 97 C 165 110 169 135 158 154 C 150 169 136 175 125 170
           C 139 167 150 157 156 141 C 161 127 158 110 148 99 Z"
        fill="#8fb3ec" opacity="0.32"/>

  <!-- 门框：内环（粗）+ 外环（细）+ 铆钉石 + 顶部拱心石 -->
  <g fill="none">
    <ellipse cx="{CX:.0f}" cy="{CY:.0f}" rx="{RX + 3:.0f}" ry="{RY + 3:.0f}"
             stroke="url(#goldEdge)" stroke-width="8"/>
    <ellipse cx="{CX:.0f}" cy="{CY:.0f}" rx="{outer_rx:.0f}" ry="{outer_ry:.0f}"
             stroke="#8a6d2f" stroke-width="4"/>
  </g>
  {studs_svg}

  <!-- 旅伴萤火：门右上方悬浮的金色光点与四芒星 -->
  <circle cx="207" cy="57" r="19" fill="url(#glow)"/>
  <circle cx="207" cy="57" r="6.5" fill="#ffd977"/>
  <path d="M 207 43 L 210 54 L 221 57 L 210 60 L 207 71 L 204 60 L 193 57 L 204 54 Z"
        fill="#ffe6a6" opacity="0.92"/>
</svg>
'''


if __name__ == "__main__":
    out = Path(__file__).resolve().parent / "icon.svg"
    out.write_text(build_svg(), encoding="utf-8")
    print(f"wrote {out}")
