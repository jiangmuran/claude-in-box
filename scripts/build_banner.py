"""Compose the README banner from logo.png + wordmark."""
from PIL import Image, ImageDraw, ImageFont
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LOGO = ROOT / "logo.png"
OUT = ROOT / "assets" / "banner.png"
OUT.parent.mkdir(parents=True, exist_ok=True)

W, H = 1600, 500
CREAM = (245, 240, 232)
CORAL = (217, 119, 87)
CORAL_DARK = (184, 90, 61)
INK = (74, 58, 46)

canvas = Image.new("RGB", (W, H), CREAM)
logo = Image.open(LOGO).convert("RGBA")
LOGO_SIZE = 360
logo = logo.resize((LOGO_SIZE, LOGO_SIZE), Image.LANCZOS)
canvas.paste(logo, (90, (H - LOGO_SIZE) // 2), logo)

draw = ImageDraw.Draw(canvas)
wordmark_font = ImageFont.truetype("/System/Library/Fonts/Menlo.ttc", 96)
tagline_font = ImageFont.truetype("/System/Library/Fonts/HelveticaNeue.ttc", 36)

text_x = 90 + LOGO_SIZE + 60
draw.text((text_x, 175), "claude-in-box", font=wordmark_font, fill=CORAL_DARK)
draw.text(
    (text_x, 285),
    "Portable Claude Code dev environment with",
    font=tagline_font,
    fill=INK,
)
draw.text(
    (text_x, 330),
    "sessions, hooks, and a web API.",
    font=tagline_font,
    fill=INK,
)

canvas.save(OUT, "PNG", optimize=True)
print(f"wrote {OUT}")
