# Brand icons

The three Icon Composer projects are the source of truth for full application icons:

- `dev/app-icon.icon`
- `nightly/app-icon.icon`
- `prod/app-icon.icon`

Each project uses `text.svg` for the AgentTunnel mark and `background.svg` when the background is a
vector layer. Additional layers use semantic names that describe their role and placement.

Run `vp run icons:export` from the repository root to regenerate the tracked iOS, Linux, Windows,
and web assets. The development web exports are also copied to `apps/web/public` for the browser
favicon and splash screen. Verify the generated assets and public copies match their sources without
changing files with `node scripts/render-brand-icons.ts --check`, the check for the fallback-rendered
rasters committed here (see below). `vp run icons:check` remains the canonical check on a maintainer
Mac with Icon Composer, and is what CI runs.

Exporting requires Icon Composer 2 or newer on macOS. The script selects the newest compatible
exporter from Xcode or a standalone Icon Composer installation and pins design generation 26. Set
`ICON_COMPOSER_TOOL` to the full path of `Icon Composer.app/Contents/Executables/ictool` to
override automatic discovery.

## Sharp fallback renderer

Icon Composer is not available everywhere, so `scripts/render-brand-icons.ts` renders the same
assets from the same projects with sharp:

```sh
node scripts/render-brand-icons.ts            # rewrite every tracked raster
node scripts/render-brand-icons.ts --check    # verify the committed rasters, exit 1 on mismatch
node scripts/render-brand-icons.ts --variant=production
```

The script reads each project's `icon.json`, inlines its layer SVGs into one flattened 1024pt
canvas at the layer scale and translation the project declares, rasterizes with sharp, and encodes
the Windows and web ICOs with the same `encodePngIco` encoder `export-brand-icons.ts` uses. It
writes, for each channel, the two 1024 full-bleed PNGs, the macOS PNG, the 180px apple-touch PNG,
the 16px and 32px favicon PNGs, and the two byte-identical seven-rendition ICOs, plus the four
`apps/web/public` copies of the development web icons.

**The committed rasters in this repository are the fallback output.** They are what
`node scripts/render-brand-icons.ts --check` reproduces byte for byte. The fallback is pinned to
the platform it last ran on: sharp 0.34.5 with its bundled librsvg, so a different sharp build can
produce different pixels. Run `vp run icons:export` on a maintainer Mac to go back to Icon Composer
output, and expect `--check` to fail until the fallback renderer has run again.

Two shapes come out of each project:

- Full bleed (iOS, universal, web, ICO): the layers composited edge to edge on the project's
  `fill`, fully opaque. iOS, Android, and Windows apply their own icon masks, so baking a platform
  mask in would only double-mask the corners. This also means the `Background` layer's own rounded
  clip is dropped when flattening; the platform mask owns the outer shape.
- macOS: the artwork scaled into the classic 824×824 safe area, masked by a continuous-corner
  ("squircle") path, with a soft drop shadow on a transparent 1024 canvas.

### What the fallback does not reproduce

The fallback composites the vector layers only. It does not reproduce Icon Composer's materials, so
its rasters are not byte-identical to a native export:

- The Liquid Glass material behind `"glass": true`, the group `"shadow"`, and
  `"translucency"` are ignored. On the flat black production icon this is invisible, but it lifts
  the development and nightly gradients by roughly 11–13/255 per channel.
- Output is 8-bit sRGB. The previous development exports were 16-bit with an embedded Display P3
  profile.
- The macOS body shape and shadow are fitted to Icon Composer's own exports (824pt body, 214pt
  continuous corner at 0.62 smoothing, shadow inset 10pt, blurred 18.6pt, offset 7pt down at 50%
  opacity) rather than produced by it. Edge alpha agrees within a few pixels.

## macOS exports

Icon Composer's command-line exporter does not expose the `macOS pre-Tahoe` preset. A plain
command-line `macOS` export is full bleed and is not suitable for the desktop app, so the export
script intentionally leaves the tracked macOS PNGs unchanged and prints a reminder after every run.

After changing an Icon Composer project, open it in Icon Composer and export the macOS PNG with
exactly these settings:

- Platform: `macOS pre-Tahoe`
- Appearance: `Default`
- Size: `1024pt`
- Scale: `1×`

Save the three exports to:

- `dev/app-icon.icon` -> `dev/blueprint-macos-1024.png`
- `nightly/app-icon.icon` -> `nightly/nightly-macos-1024.png`
- `prod/app-icon.icon` -> `prod/black-macos-1024.png`

The result must be a 1024×1024 PNG with the classic macOS safe area: the opaque icon body is
824×824, inset 100 pixels on every side, with only the native Icon Composer shadow extending into
the surrounding transparent canvas. The sharp fallback produces the same safe area and a shadow
fitted to it, so it is an acceptable substitute when Icon Composer is unavailable.

To have Codex perform the native exports, paste this prompt into a task opened at the repository
root:

```text
Use [@Computer](plugin://computer-use@openai-bundled) and the Icon Composer app to export the three macOS app icons in this repository.

For each project below, use Platform: macOS pre-Tahoe, Appearance: Default, Size: 1024pt, and Scale: 1×, then save the PNG to the exact destination:

- assets/dev/app-icon.icon -> assets/dev/blueprint-macos-1024.png
- assets/nightly/app-icon.icon -> assets/nightly/nightly-macos-1024.png
- assets/prod/app-icon.icon -> assets/prod/black-macos-1024.png

Do not resize, composite, or otherwise post-process the exported PNGs.

Verify every result is 1024×1024 and has the classic macOS safe area: an 824×824 opaque body inset 100px on every side, with only Icon Composer's native shadow extending beyond it.
```

Do not edit the generated PNG or ICO files directly.

## Android adaptive foreground

`apps/mobile/assets/android-icon-foreground.svg` is the source of truth for the foreground used by
the normal Android adaptive launcher icon. Export its paired PNG after changing it, with
`rsvg-convert` if you have it:

```sh
rsvg-convert -w 432 -h 432 \
  -o apps/mobile/assets/android-icon-foreground.png \
  apps/mobile/assets/android-icon-foreground.svg
```

Otherwise rasterize the same SVG with sharp, which `scripts` already depends on:

```sh
cd scripts && node -e "require('sharp')('../apps/mobile/assets/android-icon-foreground.svg').resize(432, 432).png().toFile('../apps/mobile/assets/android-icon-foreground.png')"
```

The foreground must remain transparent and keep the AgentTunnel mark inside Android's adaptive-icon
safe zone. `android-icon-mark.png` remains a flat silhouette for Android's monochrome themed icon.
