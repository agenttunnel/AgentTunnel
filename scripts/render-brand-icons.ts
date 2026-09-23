#!/usr/bin/env node

/**
 * Renders the tracked brand rasters directly from the Icon Composer projects.
 *
 * `export-brand-icons.ts` is the canonical path and requires Icon Composer 2.x on macOS. This
 * script is the fallback for machines that do not have it: it flattens each project's `icon.json`
 * layers into a single SVG, rasterizes with sharp, and rebuilds the ICOs with the same
 * `encodePngIco` encoder the canonical path uses. `--check` recomputes every output and
 * byte-compares it against the committed file, so the rasters stay provably in sync with the
 * sources they were rendered from.
 *
 * Two shapes come out of each project:
 *
 * - full bleed (iOS, universal, web): the layers composited edge to edge on the project's `fill`,
 *   fully opaque. iOS and Android apply their own icon masks, so baking a platform mask in would
 *   only double-mask the corners.
 * - macOS: the same artwork scaled into the classic 824x824 safe area, masked by a squircle and
 *   given a soft drop shadow on a transparent 1024 canvas.
 */

import * as NodeRuntime from "@effect/platform-node/NodeRuntime";
import * as NodeServices from "@effect/platform-node/NodeServices";
import * as Console from "effect/Console";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";
import * as Schema from "effect/Schema";
import { Command, Flag } from "effect/unstable/cli";
import sharp from "sharp";

import { BRAND_ASSET_PATHS, DEVELOPMENT_PUBLIC_ICON_OVERRIDES } from "./lib/brand-assets.ts";
import { encodePngIco, readPngDimensions, WINDOWS_ICON_SIZES } from "./lib/icon-export.ts";

const ICON_CANVAS_SIZE = 1024;
const MACOS_BODY_SIZE = 824;
const MACOS_BODY_INSET = (ICON_CANVAS_SIZE - MACOS_BODY_SIZE) / 2;
const ARTWORK_TO_MACOS_BODY_SCALE = MACOS_BODY_SIZE / ICON_CANVAS_SIZE;

/**
 * Corner geometry of the macOS icon body, fitted against Icon Composer's own `macOS pre-Tahoe`
 * exports: the 824pt body is a continuous ("squircle") corner with a 214pt radius and 0.62
 * smoothing. The shadow is the same shape inset 10pt, blurred and nudged down, which reproduces
 * the native alpha falloff to within a couple of pixels.
 */
const MACOS_CORNER_RADIUS = 214;
const MACOS_CORNER_SMOOTHING = 0.62;
const MACOS_SHADOW_INSET = 10;
const MACOS_SHADOW_OFFSET_Y = 7;
const MACOS_SHADOW_BLUR = 18.6;
const MACOS_SHADOW_OPACITY = 0.5;

interface VariantOutputs {
  readonly ios: string;
  readonly macos: string;
  readonly universal: string;
  readonly appleTouch: string;
  readonly favicon16: string;
  readonly favicon32: string;
  readonly faviconIco: string;
  readonly windowsIco: string;
}

interface IconVariant {
  readonly label: string;
  readonly source: string;
  readonly outputs: VariantOutputs;
}

const ICON_VARIANTS = [
  {
    label: "development",
    source: BRAND_ASSET_PATHS.developmentIconComposerProject,
    outputs: {
      ios: BRAND_ASSET_PATHS.developmentIosIconPng,
      macos: BRAND_ASSET_PATHS.developmentDesktopIconPng,
      universal: BRAND_ASSET_PATHS.developmentUniversalIconPng,
      appleTouch: BRAND_ASSET_PATHS.developmentWebAppleTouchIconPng,
      favicon16: BRAND_ASSET_PATHS.developmentWebFavicon16Png,
      favicon32: BRAND_ASSET_PATHS.developmentWebFavicon32Png,
      faviconIco: BRAND_ASSET_PATHS.developmentWebFaviconIco,
      windowsIco: BRAND_ASSET_PATHS.developmentWindowsIconIco,
    },
  },
  {
    label: "nightly",
    source: BRAND_ASSET_PATHS.nightlyIconComposerProject,
    outputs: {
      ios: BRAND_ASSET_PATHS.nightlyIosIconPng,
      macos: BRAND_ASSET_PATHS.nightlyMacIconPng,
      universal: BRAND_ASSET_PATHS.nightlyLinuxIconPng,
      appleTouch: BRAND_ASSET_PATHS.nightlyWebAppleTouchIconPng,
      favicon16: BRAND_ASSET_PATHS.nightlyWebFavicon16Png,
      favicon32: BRAND_ASSET_PATHS.nightlyWebFavicon32Png,
      faviconIco: BRAND_ASSET_PATHS.nightlyWebFaviconIco,
      windowsIco: BRAND_ASSET_PATHS.nightlyWindowsIconIco,
    },
  },
  {
    label: "production",
    source: BRAND_ASSET_PATHS.productionIconComposerProject,
    outputs: {
      ios: BRAND_ASSET_PATHS.productionIosIconPng,
      macos: BRAND_ASSET_PATHS.productionMacIconPng,
      universal: BRAND_ASSET_PATHS.productionLinuxIconPng,
      appleTouch: BRAND_ASSET_PATHS.productionWebAppleTouchIconPng,
      favicon16: BRAND_ASSET_PATHS.productionWebFavicon16Png,
      favicon32: BRAND_ASSET_PATHS.productionWebFavicon32Png,
      faviconIco: BRAND_ASSET_PATHS.productionWebFaviconIco,
      windowsIco: BRAND_ASSET_PATHS.productionWindowsIconIco,
    },
  },
] as const satisfies ReadonlyArray<IconVariant>;

const IconProjectLayer = Schema.Struct({
  name: Schema.String,
  "image-name": Schema.String,
  hidden: Schema.optional(Schema.Boolean),
  opacity: Schema.optional(Schema.Number),
  position: Schema.Struct({
    scale: Schema.Number,
    "translation-in-points": Schema.optional(Schema.Tuple([Schema.Number, Schema.Number])),
  }),
});

const IconProjectDocument = Schema.Struct({
  fill: Schema.Union([
    Schema.Struct({ solid: Schema.String }),
    Schema.Struct({ "automatic-gradient": Schema.String }),
  ]),
  groups: Schema.Array(Schema.Struct({ layers: Schema.Array(IconProjectLayer) })),
});

const decodeIconProject = Schema.decodeUnknownEffect(Schema.fromJsonString(IconProjectDocument));

export class BrandIconSourceError extends Schema.TaggedErrorClass<BrandIconSourceError>()(
  "BrandIconSourceError",
  {
    operation: Schema.Literals(["read-project", "read-layer", "parse-project", "parse-layer"]),
    path: Schema.String,
    cause: Schema.Defect(),
  },
) {
  override get message(): string {
    return `Brand icon source operation '${this.operation}' failed for ${this.path}.`;
  }
}

export class BrandIconRenderError extends Schema.TaggedErrorClass<BrandIconRenderError>()(
  "BrandIconRenderError",
  {
    operation: Schema.Literals(["rasterize", "encode-ico", "read-output", "write-output"]),
    path: Schema.String,
    cause: Schema.Defect(),
  },
) {
  override get message(): string {
    return `Brand icon render operation '${this.operation}' failed for ${this.path}.`;
  }
}

export class BrandIconAssetsStaleError extends Schema.TaggedErrorClass<BrandIconAssetsStaleError>()(
  "BrandIconAssetsStaleError",
  {
    paths: Schema.Array(Schema.String),
  },
) {
  override get message(): string {
    return `Generated icon assets are stale:\n${this.paths.map((path) => `- ${path}`).join("\n")}`;
  }
}

export class BrandIconUnknownVariantError extends Schema.TaggedErrorClass<BrandIconUnknownVariantError>()(
  "BrandIconUnknownVariantError",
  {
    label: Schema.String,
    known: Schema.Array(Schema.String),
  },
) {
  override get message(): string {
    return `Unknown brand icon variant '${this.label}'. Known variants: ${this.known.join(", ")}.`;
  }
}

/** Trims float noise so identical inputs always produce identical SVG text. */
const formatNumber = (value: number): string => String(Number(value.toFixed(4)));

const toRadians = (degrees: number): number => (degrees * Math.PI) / 180;

interface CornerParams {
  readonly a: number;
  readonly b: number;
  readonly c: number;
  readonly d: number;
  readonly p: number;
  readonly arcSectionLength: number;
  readonly cornerRadius: number;
}

/**
 * Continuous-corner ("squircle") geometry, from Figma's corner smoothing construction. macOS icon
 * bodies are squircle-shaped rather than plain rounded rectangles, and the difference is visible
 * in the Dock.
 */
const cornerParams = (
  cornerRadius: number,
  cornerSmoothing: number,
  roundingAndSmoothingBudget: number,
): CornerParams => {
  const smoothing = Math.min(cornerSmoothing, roundingAndSmoothingBudget / cornerRadius - 1);
  const p = Math.min((1 + cornerSmoothing) * cornerRadius, roundingAndSmoothingBudget);
  const arcMeasure = 90 * (1 - smoothing);
  const arcSectionLength = Math.sin(toRadians(arcMeasure / 2)) * cornerRadius * Math.sqrt(2);
  const angleAlpha = (90 - arcMeasure) / 2;
  const p3ToP4Distance = cornerRadius * Math.tan(toRadians(angleAlpha / 2));
  const angleBeta = 45 * smoothing;
  const c = p3ToP4Distance * Math.cos(toRadians(angleBeta));
  const d = c * Math.tan(toRadians(angleBeta));
  const b = (p - arcSectionLength - c - d) / 3;
  return { a: 2 * b, b, c, d, p, arcSectionLength, cornerRadius };
};

const squirclePath = (size: number, params: CornerParams): string => {
  const { a, b, c, d, p, arcSectionLength, cornerRadius } = params;
  const n = formatNumber;
  const r = n(cornerRadius);
  const arc = n(arcSectionLength);
  return [
    `M ${n(size - p)} 0`,
    `c ${n(a)} 0 ${n(a + b)} 0 ${n(a + b + c)} ${n(d)}`,
    `a ${r} ${r} 0 0 1 ${arc} ${arc}`,
    `c ${n(d)} ${n(c)} ${n(d)} ${n(b + c)} ${n(d)} ${n(a + b + c)}`,
    `L ${n(size)} ${n(size - p)}`,
    `c 0 ${n(a)} 0 ${n(a + b)} ${n(-d)} ${n(a + b + c)}`,
    `a ${r} ${r} 0 0 1 ${n(-arcSectionLength)} ${arc}`,
    `c ${n(-c)} ${n(d)} ${n(-(b + c))} ${n(d)} ${n(-(a + b + c))} ${n(d)}`,
    `L ${n(p)} ${n(size)}`,
    `c ${n(-a)} 0 ${n(-(a + b))} 0 ${n(-(a + b + c))} ${n(-d)}`,
    `a ${r} ${r} 0 0 1 ${n(-arcSectionLength)} ${n(-arcSectionLength)}`,
    `c ${n(-d)} ${n(-c)} ${n(-d)} ${n(-(b + c))} ${n(-d)} ${n(-(a + b + c))}`,
    `L 0 ${n(p)}`,
    `c 0 ${n(-a)} 0 ${n(-(a + b))} ${n(d)} ${n(-(a + b + c))}`,
    `a ${r} ${r} 0 0 1 ${arc} ${n(-arcSectionLength)}`,
    `c ${n(c)} ${n(-d)} ${n(b + c)} ${n(-d)} ${n(a + b + c)} ${n(-d)}`,
    "Z",
  ].join(" ");
};

/** Prefixes every `id` and `url(#…)` reference so inlined layers cannot collide. */
const namespaceSvgIds = (svg: string, namespace: string): string => {
  const ids = new Set<string>();
  for (const match of svg.matchAll(/\bid="([^"]+)"/g)) {
    const id = match[1];
    if (id !== undefined) ids.add(id);
  }
  let output = svg;
  for (const id of [...ids].sort((left, right) => right.length - left.length)) {
    const scoped = `${namespace}-${id}`;
    output = output
      .replaceAll(`id="${id}"`, `id="${scoped}"`)
      .replaceAll(`url(#${id})`, `url(#${scoped})`)
      .replaceAll(`href="#${id}"`, `href="#${scoped}"`);
  }
  return output;
};

interface LayerGeometry {
  readonly width: number;
  readonly height: number;
  readonly rootAttributes: string;
  readonly body: string;
}

/**
 * Root presentation attributes (`fill="none"` in every project layer) are kept so inlined layers
 * still resolve unstyled fills to `none` rather than to the SVG default of black.
 */
const ROOT_ATTRIBUTES_TO_DROP = new Set(["width", "height", "viewBox", "xmlns"]);

const readLayerGeometry = (svg: string, path: string): LayerGeometry => {
  const openTagEnd = svg.indexOf(">");
  const openTag = openTagEnd === -1 ? "" : svg.slice(0, openTagEnd + 1);
  const width = Number(/\bwidth="([\d.]+)"/.exec(openTag)?.[1]);
  const height = Number(/\bheight="([\d.]+)"/.exec(openTag)?.[1]);
  const body = openTagEnd === -1 ? "" : svg.slice(openTagEnd + 1, svg.lastIndexOf("</svg>"));
  if (!Number.isFinite(width) || width <= 0 || !Number.isFinite(height) || height <= 0) {
    throw new Error(`Layer ${path} needs explicit width and height attributes.`);
  }
  if (body.trim().length === 0) {
    throw new Error(`Layer ${path} has no drawable content.`);
  }
  const rootAttributes: string[] = [];
  for (const match of openTag.matchAll(/([\w:-]+)="([^"]*)"/g)) {
    const name = match[1];
    const value = match[2];
    if (name === undefined || value === undefined) continue;
    if (ROOT_ATTRIBUTES_TO_DROP.has(name) || name.startsWith("xmlns:")) continue;
    rootAttributes.push(`${name}="${value}"`);
  }
  return { width, height, rootAttributes: rootAttributes.join(" "), body };
};

/** `display-p3:0,0,0,1` (and the `automatic-gradient` spelling of the same value) as `#rrggbb`. */
const resolveFillColor = (fill: typeof IconProjectDocument.Type.fill): string => {
  const value = "solid" in fill ? fill.solid : fill["automatic-gradient"];
  const match = /^[a-z-]*:?\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/.exec(value);
  if (match === null) return value;
  const channel = (index: number): string => {
    const parsed = Number(match[index]);
    const clamped = Math.max(0, Math.min(1, Number.isFinite(parsed) ? parsed : 0));
    return Math.round(clamped * 255)
      .toString(16)
      .padStart(2, "0");
  };
  return `#${channel(1)}${channel(2)}${channel(3)}`;
};

interface Artwork {
  readonly fillColor: string;
  readonly markup: string;
}

const RepositoryRoot = Effect.service(Path.Path).pipe(
  Effect.flatMap((path) => path.fromFileUrl(new URL("..", import.meta.url))),
  Effect.mapError(
    (cause) =>
      new BrandIconSourceError({
        operation: "read-project",
        path: new URL("..", import.meta.url).pathname,
        cause,
      }),
  ),
);

const readIconProject = Effect.fn("renderBrandIcons.readIconProject")(function* (
  repositoryRoot: string,
  variant: IconVariant,
) {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const relativePath = path.join(variant.source, "icon.json");
  const projectJson = yield* fs
    .readFileString(path.join(repositoryRoot, relativePath))
    .pipe(
      Effect.mapError(
        (cause) =>
          new BrandIconSourceError({ operation: "read-project", path: relativePath, cause }),
      ),
    );
  return yield* decodeIconProject(projectJson).pipe(
    Effect.mapError(
      (cause) =>
        new BrandIconSourceError({ operation: "parse-project", path: relativePath, cause }),
    ),
  );
});

/**
 * Flattens a project's layers into one SVG body in the 1024pt Icon Composer canvas. Layers are
 * listed front-to-back in `icon.json`, so they are painted in reverse. The Background layer's own
 * rounded clip is dropped: the macOS squircle and the platform masks own the outer shape, and
 * dropping it keeps the full-bleed renditions opaque edge to edge.
 */
const buildArtwork = Effect.fn("renderBrandIcons.buildArtwork")(function* (
  repositoryRoot: string,
  variant: IconVariant,
) {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const project = yield* readIconProject(repositoryRoot, variant);
  const layers = project.groups
    .flatMap((group) => group.layers)
    .filter((layer) => layer.hidden !== true);

  const painted = yield* Effect.forEach(
    layers,
    (layer, index) =>
      Effect.gen(function* () {
        const relativePath = path.join(variant.source, "Assets", layer["image-name"]);
        const svg = yield* fs
          .readFileString(path.join(repositoryRoot, relativePath))
          .pipe(
            Effect.mapError(
              (cause) =>
                new BrandIconSourceError({ operation: "read-layer", path: relativePath, cause }),
            ),
          );
        const geometry = yield* Effect.try({
          try: () => readLayerGeometry(svg, relativePath),
          catch: (cause) =>
            new BrandIconSourceError({ operation: "parse-layer", path: relativePath, cause }),
        });
        const [translationX = 0, translationY = 0] = layer.position["translation-in-points"] ?? [];
        const scale = layer.position.scale;
        const originX = ICON_CANVAS_SIZE / 2 + translationX - (geometry.width * scale) / 2;
        const originY = ICON_CANVAS_SIZE / 2 + translationY - (geometry.height * scale) / 2;
        const body =
          layer.name === "Background"
            ? geometry.body.replaceAll(/ clip-path="url\(#[^)]+\)"/g, "")
            : geometry.body;
        const opacity =
          layer.opacity === undefined ? "" : ` opacity="${formatNumber(layer.opacity)}"`;
        return [
          `<g ${geometry.rootAttributes} transform="translate(${formatNumber(originX)} ${formatNumber(originY)}) scale(${formatNumber(scale)})"${opacity}>`,
          namespaceSvgIds(body, `${variant.label}-${index}`),
          `</g>`,
        ].join("");
      }),
    { concurrency: 1 },
  );

  return { fillColor: resolveFillColor(project.fill), markup: painted.toReversed().join("") };
});

const fullBleedDocument = (artwork: Artwork, size: number): string =>
  [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${ICON_CANVAS_SIZE} ${ICON_CANVAS_SIZE}">`,
    `<rect width="${ICON_CANVAS_SIZE}" height="${ICON_CANVAS_SIZE}" fill="${artwork.fillColor}"/>`,
    artwork.markup,
    `</svg>`,
  ].join("");

const macosDocument = (artwork: Artwork): string => {
  const bodyPath = squirclePath(
    MACOS_BODY_SIZE,
    cornerParams(MACOS_CORNER_RADIUS, MACOS_CORNER_SMOOTHING, MACOS_BODY_SIZE / 2),
  );
  const shadowSize = MACOS_BODY_SIZE - MACOS_SHADOW_INSET * 2;
  const shadowInset = (ICON_CANVAS_SIZE - shadowSize) / 2;
  const shadowPath = squirclePath(
    shadowSize,
    cornerParams(
      (MACOS_CORNER_RADIUS * shadowSize) / MACOS_BODY_SIZE,
      MACOS_CORNER_SMOOTHING,
      shadowSize / 2,
    ),
  );
  return [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${ICON_CANVAS_SIZE}" height="${ICON_CANVAS_SIZE}" viewBox="0 0 ${ICON_CANVAS_SIZE} ${ICON_CANVAS_SIZE}">`,
    `<defs>`,
    `<clipPath id="macos-body"><path d="${bodyPath}"/></clipPath>`,
    `<filter id="macos-shadow" x="-20%" y="-20%" width="140%" height="140%">`,
    `<feGaussianBlur in="SourceAlpha" stdDeviation="${MACOS_SHADOW_BLUR}" result="blur"/>`,
    `<feOffset in="blur" dy="${MACOS_SHADOW_OFFSET_Y}" result="offset"/>`,
    `<feFlood flood-color="#000000" flood-opacity="${MACOS_SHADOW_OPACITY}" result="color"/>`,
    `<feComposite in="color" in2="offset" operator="in"/>`,
    `</filter>`,
    `</defs>`,
    `<g transform="translate(${shadowInset} ${shadowInset})">`,
    `<path d="${shadowPath}" fill="#000000" filter="url(#macos-shadow)"/>`,
    `</g>`,
    `<g transform="translate(${MACOS_BODY_INSET} ${MACOS_BODY_INSET})">`,
    `<g clip-path="url(#macos-body)">`,
    `<g transform="scale(${formatNumber(ARTWORK_TO_MACOS_BODY_SCALE)})">`,
    `<rect width="${ICON_CANVAS_SIZE}" height="${ICON_CANVAS_SIZE}" fill="${artwork.fillColor}"/>`,
    artwork.markup,
    `</g></g></g></svg>`,
  ].join("");
};

const rasterize = Effect.fn("renderBrandIcons.rasterize")(function* (
  document: string,
  size: number,
  relativePath: string,
) {
  const contents = yield* Effect.tryPromise({
    try: () =>
      sharp(Buffer.from(document))
        .png({ compressionLevel: 9, adaptiveFiltering: false, palette: false })
        .toBuffer(),
    catch: (cause) =>
      new BrandIconRenderError({ operation: "rasterize", path: relativePath, cause }),
  });
  const dimensions = yield* Effect.try({
    try: () => readPngDimensions(contents),
    catch: (cause) =>
      new BrandIconRenderError({ operation: "rasterize", path: relativePath, cause }),
  });
  if (dimensions.width !== size || dimensions.height !== size) {
    return yield* new BrandIconRenderError({
      operation: "rasterize",
      path: relativePath,
      cause: new Error(`Expected ${size}x${size}, got ${dimensions.width}x${dimensions.height}.`),
    });
  }
  return contents;
});

const renderVariant = Effect.fn("renderBrandIcons.renderVariant")(function* (
  repositoryRoot: string,
  variant: IconVariant,
) {
  const artwork = yield* buildArtwork(repositoryRoot, variant);
  const fullBleed = yield* rasterize(
    fullBleedDocument(artwork, ICON_CANVAS_SIZE),
    ICON_CANVAS_SIZE,
    variant.outputs.ios,
  );
  const icoRenditions = yield* Effect.forEach(
    WINDOWS_ICON_SIZES,
    (size) =>
      rasterize(fullBleedDocument(artwork, size), size, variant.outputs.faviconIco).pipe(
        Effect.map((contents) => ({ size, contents })),
      ),
    { concurrency: 1 },
  );
  const ico = yield* Effect.try({
    try: () => encodePngIco(icoRenditions),
    catch: (cause) =>
      new BrandIconRenderError({
        operation: "encode-ico",
        path: variant.outputs.faviconIco,
        cause,
      }),
  });

  return new Map<string, Buffer>([
    [variant.outputs.ios, fullBleed],
    [variant.outputs.universal, fullBleed],
    [
      variant.outputs.macos,
      yield* rasterize(macosDocument(artwork), ICON_CANVAS_SIZE, variant.outputs.macos),
    ],
    [
      variant.outputs.appleTouch,
      yield* rasterize(fullBleedDocument(artwork, 180), 180, variant.outputs.appleTouch),
    ],
    [
      variant.outputs.favicon16,
      yield* rasterize(fullBleedDocument(artwork, 16), 16, variant.outputs.favicon16),
    ],
    [
      variant.outputs.favicon32,
      yield* rasterize(fullBleedDocument(artwork, 32), 32, variant.outputs.favicon32),
    ],
    [variant.outputs.faviconIco, ico],
    [variant.outputs.windowsIco, ico],
  ]);
});

const isCurrent = Effect.fn("renderBrandIcons.isCurrent")(function* (
  repositoryRoot: string,
  relativePath: string,
  expected: Buffer,
) {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const targetPath = path.join(repositoryRoot, relativePath);
  const exists = yield* fs
    .exists(targetPath)
    .pipe(
      Effect.mapError(
        (cause) =>
          new BrandIconRenderError({ operation: "read-output", path: relativePath, cause }),
      ),
    );
  if (!exists) return false;
  const actual = yield* fs
    .readFile(targetPath)
    .pipe(
      Effect.mapError(
        (cause) =>
          new BrandIconRenderError({ operation: "read-output", path: relativePath, cause }),
      ),
    );
  return Buffer.from(actual).equals(expected);
});

const writeOutput = Effect.fn("renderBrandIcons.writeOutput")(function* (
  repositoryRoot: string,
  relativePath: string,
  contents: Buffer,
) {
  const fs = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const targetPath = path.join(repositoryRoot, relativePath);
  yield* fs
    .makeDirectory(path.dirname(targetPath), { recursive: true })
    .pipe(
      Effect.mapError(
        (cause) =>
          new BrandIconRenderError({ operation: "write-output", path: relativePath, cause }),
      ),
    );
  yield* fs
    .writeFile(targetPath, contents)
    .pipe(
      Effect.mapError(
        (cause) =>
          new BrandIconRenderError({ operation: "write-output", path: relativePath, cause }),
      ),
    );
});

export const renderBrandIcons = Effect.fn("renderBrandIcons")(function* (
  checkOnly: boolean,
  variantLabel: string | undefined,
) {
  const repositoryRoot = yield* RepositoryRoot;
  const variants =
    variantLabel === undefined
      ? ICON_VARIANTS
      : ICON_VARIANTS.filter((variant) => variant.label === variantLabel);
  if (variants.length === 0) {
    return yield* new BrandIconUnknownVariantError({
      label: variantLabel ?? "",
      known: ICON_VARIANTS.map((variant) => variant.label),
    });
  }

  const generated = new Map<string, Buffer>();
  for (const variant of variants) {
    yield* Console.log(`Rendering ${variant.label} from ${variant.source}...`);
    for (const [relativePath, contents] of yield* renderVariant(repositoryRoot, variant)) {
      generated.set(relativePath, contents);
    }
  }

  if (variantLabel === undefined) {
    for (const override of DEVELOPMENT_PUBLIC_ICON_OVERRIDES) {
      const contents = generated.get(override.sourceRelativePath);
      if (contents === undefined) {
        return yield* Effect.die(
          new Error(`Generated development web icon is missing: ${override.sourceRelativePath}`),
        );
      }
      generated.set(override.targetRelativePath, contents);
    }
  }

  if (checkOnly) {
    const stale = yield* Effect.filter(
      [...generated.entries()],
      ([relativePath, contents]) =>
        isCurrent(repositoryRoot, relativePath, contents).pipe(Effect.map((current) => !current)),
      { concurrency: "unbounded" },
    );
    if (stale.length > 0) {
      return yield* new BrandIconAssetsStaleError({
        paths: stale.map(([relativePath]) => relativePath),
      });
    }
    yield* Console.log(`All ${generated.size} generated icon assets are current.`);
    return;
  }

  yield* Effect.forEach(
    generated,
    ([relativePath, contents]) => writeOutput(repositoryRoot, relativePath, contents),
    { concurrency: 1, discard: true },
  );
  yield* Console.log(`Updated ${generated.size} generated icon assets.`);
});

export const renderBrandIconsCommand = Command.make(
  "render-brand-icons",
  {
    check: Flag.boolean("check").pipe(
      Flag.withDescription("Verify generated icon assets without modifying files."),
      Flag.withDefault(false),
    ),
    variant: Flag.string("variant").pipe(
      Flag.withDescription("Render a single variant: development, nightly, or production."),
      Flag.optional,
    ),
  },
  ({ check, variant }) =>
    renderBrandIcons(
      check,
      Option.getOrElse(variant, () => undefined),
    ),
).pipe(
  Command.withDescription(
    "Render tracked brand rasters from the Icon Composer projects without Icon Composer.",
  ),
);

if (import.meta.main) {
  Command.run(renderBrandIconsCommand, { version: "0.0.0" }).pipe(
    Effect.provide(NodeServices.layer),
    NodeRuntime.runMain,
  );
}
