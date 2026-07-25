# ACPP icon — "Harness"

Accent `#47c98a` · glyph on a 128 grid, 18px safe margin.

## Linux
Copy `linux/hicolor/<size>/apps/acpp.png` into `~/.local/share/icons/hicolor/...`
(or `/usr/share/icons/hicolor/...`), then `gtk-update-icon-cache`.
Scalable: `acpp.svg` → `hicolor/scalable/apps/acpp.svg`.
Monochrome panel/tray variant: `acpp-symbolic.svg` (uses `currentColor`).
Set `Icon=acpp` in the `.desktop` file.

## Android
- `android/mipmap-*/ic_launcher_foreground.png` — adaptive foreground (108dp canvas, glyph inside the 66dp safe zone)
- `android/mipmap-*/ic_launcher_monochrome.png` — themed-icon layer (Android 13+)
- `android/mipmap-*/ic_launcher.png` — legacy square fallback on `#101110`
- `android/ic_launcher.xml` → `res/mipmap-anydpi-v26/`
- background color: `#101110` (add as `@color/ic_launcher_background`)
- `play-store-512.png` — store listing
