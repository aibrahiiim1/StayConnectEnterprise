"use client";

// CHARTS, AS INLINE SVG AND NOTHING ELSE.
//
// No charting library: the appliance ships this bundle to a hotel back office, and three of these shapes do not
// justify a runtime dependency. Everything below draws with the theme tokens, so a chart is correct in light and
// dark without a second palette and without reading the current theme in JavaScript.
//
// The conventions are fixed, not per-call:
//   * columns cap at 24px and keep a 2px surface-coloured gap, so touching bars separate by air, not outline;
//   * data-ends are rounded 4px and square at the baseline;
//   * gridlines are hairline, solid and one step off the surface — never dashed;
//   * a legend is always drawn for two or more series, and every chart has a hover read-out, because a chart you
//     cannot interrogate makes the reader guess at values the tooltip could simply state.

import * as React from "react";
import { cn } from "@/lib/utils";

export const SERIES = ["chart-1", "chart-2", "chart-3", "chart-4", "chart-5", "chart-6"] as const;
export type SeriesToken = (typeof SERIES)[number];

/** The fixed categorical order. Assigned by index, never cycled — a 7th series folds into "Other". */
export const seriesColor = (i: number) => `hsl(var(--${SERIES[Math.min(i, SERIES.length - 1)]}))`;

type Point = { label: string; values: number[] };

/**
 * ColumnChart — stacked or single-series columns over a categorical/time axis.
 *
 * Built for "traffic per hour" and "sign-ins per hour": a bounded number of bands, a total worth reading, and a
 * part-to-whole split (down/up) that a stacked column states directly. Deliberately NOT a dual-axis chart —
 * bytes and session counts are two measures and get two charts.
 */
export function ColumnChart({
  data,
  series,
  height = 148,
  formatValue = (n) => n.toLocaleString(),
  formatAxis,
  tickEvery = 3,
  className,
  emptyLabel = "No activity recorded",
}: {
  data: Point[];
  series: { name: string; token?: SeriesToken }[];
  height?: number;
  formatValue?: (n: number) => string;
  formatAxis?: (n: number) => string;
  /** Label every Nth band, so 24 hourly columns do not produce 24 overlapping labels. */
  tickEvery?: number;
  className?: string;
  emptyLabel?: string;
}) {
  const [hover, setHover] = React.useState<number | null>(null);

  const totals = data.map((d) => d.values.reduce((a, b) => a + (b || 0), 0));
  const peak = Math.max(0, ...totals);
  const allZero = peak <= 0;

  // A "nice" ceiling so the top gridline is a round number rather than the exact peak.
  const ceiling = allZero ? 1 : niceCeiling(peak);
  const fmtAxis = formatAxis ?? formatValue;

  return (
    <div className={cn("min-w-0", className)}>
      {series.length > 1 && <Legend items={series.map((s, i) => ({ name: s.name, index: i, token: s.token }))} />}

      <div className="relative">
        {/* Gridlines sit behind the columns in their own absolutely-positioned layer so the bar geometry stays
            simple arithmetic and the labels are real text rather than SVG <text> that cannot wrap. */}
        <div className="pointer-events-none absolute inset-0 flex flex-col justify-between" style={{ height }}>
          {[1, 0.5, 0].map((f) => (
            <div key={f} className="flex items-center gap-2">
              <span className="w-12 shrink-0 text-right text-2xs tabular text-muted-foreground/70">
                {fmtAxis(Math.round(ceiling * f))}
              </span>
              <span className="h-px flex-1 bg-border" />
            </div>
          ))}
        </div>

        <div className="flex items-end gap-[2px] pl-14" style={{ height }} role="img"
          aria-label={`${series.map((s) => s.name).join(" and ")} by ${data.length === 24 ? "hour" : "period"}`}>
          {data.map((d, i) => {
            const total = totals[i];
            const active = hover === i;
            return (
              <button
                key={i}
                type="button"
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover((h) => (h === i ? null : h))}
                onFocus={() => setHover(i)}
                onBlur={() => setHover((h) => (h === i ? null : h))}
                // The hit target is the FULL column height, not the drawn bar: an hour with 40 KB of traffic
                // draws two pixels, and a two-pixel hover target is the same as none.
                className={cn(
                  "group relative flex h-full max-w-6 flex-1 cursor-default flex-col justify-end rounded-sm",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                  active && "bg-surface/60",
                )}
                aria-label={`${d.label}: ${series
                  .map((s, si) => `${s.name} ${formatValue(d.values[si] ?? 0)}`)
                  .join(", ")}`}
              >
                {/* Stacked top-down so the first series sits at the bottom of the column. */}
                {[...series].map((s, si) => series.length - 1 - si).map((si) => {
                  const v = d.values[si] ?? 0;
                  if (v <= 0) return null;
                  const pct = (v / ceiling) * 100;
                  const isTop = si === topIndex(d.values);
                  return (
                    <span
                      key={si}
                      style={{
                        height: `${Math.max(pct, 0.9)}%`,
                        background: seriesColor(si),
                        // The 2px surface gap between stacked segments — air, not a stroke.
                        marginBottom: si > 0 ? 2 : 0,
                      }}
                      className={cn(
                        "block w-full transition-opacity",
                        isTop ? "rounded-t-[4px]" : "",
                        hover !== null && !active && "opacity-55",
                      )}
                    />
                  );
                })}
                {allZero && <span className="block h-px w-full bg-border-strong" />}

                {active && total > 0 && (
                  <ChartTooltip label={d.label}>
                    {series.map((s, si) => (
                      <Row key={si} swatch={seriesColor(si)} name={s.name} value={formatValue(d.values[si] ?? 0)} />
                    ))}
                    {series.length > 1 && (
                      <Row name="Total" value={formatValue(total)} strong />
                    )}
                  </ChartTooltip>
                )}
              </button>
            );
          })}
        </div>
      </div>

      <div className="mt-1.5 flex gap-[2px] pl-14">
        {data.map((d, i) => (
          <span
            key={i}
            className="max-w-6 flex-1 truncate text-center text-2xs tabular text-muted-foreground/70"
          >
            {i % tickEvery === 0 ? d.label : ""}
          </span>
        ))}
      </div>

      {allZero && (
        <p className="mt-2 text-center text-xs text-muted-foreground">{emptyLabel}</p>
      )}
    </div>
  );
}

/** The index of the topmost non-zero segment, so only it gets the rounded cap. */
function topIndex(values: number[]): number {
  for (let i = values.length - 1; i >= 0; i--) if ((values[i] ?? 0) > 0) return i;
  return 0;
}

/**
 * Sparkline — a 2px trend line for a stat tile. One series, so no legend: the tile's label says what it is.
 * No axis, no labels, no tooltip; the figure beside it carries the value.
 */
export function Sparkline({
  values,
  width = 96,
  height = 28,
  token = "chart-1",
  className,
}: {
  values: number[];
  width?: number;
  height?: number;
  token?: SeriesToken;
  className?: string;
}) {
  if (values.length < 2) return null;
  const max = Math.max(...values, 1);
  const step = width / (values.length - 1);
  const y = (v: number) => height - 2 - (v / max) * (height - 4);
  const line = values.map((v, i) => `${i === 0 ? "M" : "L"}${(i * step).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const area = `${line} L${width},${height} L0,${height} Z`;
  const color = `hsl(var(--${token}))`;
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={cn("overflow-visible", className)}
      aria-hidden
    >
      {/* The area is the hue at ~10% — a wash that gives the line a base without competing with it. */}
      <path d={area} fill={color} opacity={0.1} />
      <path d={line} fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" />
      <circle cx={width} cy={y(values[values.length - 1])} r={2.5} fill={color} />
    </svg>
  );
}

/**
 * SplitBar — one horizontal stacked bar for a part-to-whole with a handful of named parts.
 *
 * This is the form a donut is usually reached for and is wrong for: the parts are a proportion of one total,
 * the names are long, and the reader needs to compare two of them — all of which a straight bar does better.
 * Every part is labelled in the legend with its own count, so nothing depends on reading an area by eye.
 */
export function SplitBar({
  parts,
  total,
  className,
  formatValue = (n) => n.toLocaleString(),
}: {
  parts: { name: string; value: number; tone?: "ok" | "warn" | "err" | "info" | "neutral"; token?: SeriesToken }[];
  total?: number;
  className?: string;
  formatValue?: (n: number) => string;
}) {
  const sum = total ?? parts.reduce((a, p) => a + p.value, 0);
  const shown = parts.filter((p) => p.value > 0);
  const toneColor: Record<string, string> = {
    ok: "hsl(var(--success))",
    warn: "hsl(var(--warning))",
    err: "hsl(var(--destructive))",
    info: "hsl(var(--info))",
    neutral: "hsl(var(--muted-foreground) / 0.45)",
  };
  const colorOf = (p: (typeof parts)[number], i: number) =>
    p.tone ? toneColor[p.tone] : p.token ? `hsl(var(--${p.token}))` : seriesColor(i);

  return (
    <div className={cn("space-y-2", className)}>
      <div className="flex h-2 w-full gap-[2px] overflow-hidden rounded-full bg-surface">
        {sum > 0 ? (
          shown.map((p, i) => (
            <span
              key={p.name}
              title={`${p.name}: ${formatValue(p.value)}`}
              style={{ width: `${(p.value / sum) * 100}%`, background: colorOf(p, parts.indexOf(p)) }}
              className="block first:rounded-l-full last:rounded-r-full"
            />
          ))
        ) : null}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1">
        {parts.map((p, i) => (
          <li key={p.name} className="inline-flex items-center gap-1.5 text-xs">
            <span
              className="size-2 shrink-0 rounded-full"
              style={{ background: colorOf(p, i) }}
              aria-hidden
            />
            {/* The count is stated, never inferred from the bar — and the text stays in ink, not the mark's hue. */}
            <span className="text-muted-foreground">{p.name}</span>
            <span className="font-medium tabular text-foreground">{formatValue(p.value)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Legend({ items }: { items: { name: string; index: number; token?: SeriesToken }[] }) {
  return (
    <ul className="mb-2.5 flex flex-wrap gap-x-4 gap-y-1 pl-14">
      {items.map((it) => (
        <li key={it.name} className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          <span
            className="h-0.5 w-3 shrink-0 rounded-full"
            style={{ background: it.token ? `hsl(var(--${it.token}))` : seriesColor(it.index) }}
            aria-hidden
          />
          {it.name}
        </li>
      ))}
    </ul>
  );
}

function ChartTooltip({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div
      className={cn(
        "pointer-events-none absolute bottom-full left-1/2 z-20 mb-2 w-max -translate-x-1/2",
        "rounded-md border border-border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md",
      )}
    >
      <div className="mb-1 text-2xs font-semibold uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function Row({
  swatch, name, value, strong,
}: { swatch?: string; name: string; value: string; strong?: boolean }) {
  return (
    <div className="flex items-center gap-2 whitespace-nowrap text-xs">
      {swatch ? (
        <span className="size-1.5 shrink-0 rounded-full" style={{ background: swatch }} aria-hidden />
      ) : (
        <span className="size-1.5 shrink-0" aria-hidden />
      )}
      <span className="text-muted-foreground">{name}</span>
      <span className={cn("ml-auto tabular", strong ? "font-semibold" : "font-medium")}>{value}</span>
    </div>
  );
}

/** A round ceiling at or above the peak, so the top gridline reads 2 GB rather than 1.87 GB. */
function niceCeiling(peak: number): number {
  if (peak <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(peak)));
  const scaled = peak / mag;
  const step = scaled <= 1 ? 1 : scaled <= 2 ? 2 : scaled <= 2.5 ? 2.5 : scaled <= 5 ? 5 : 10;
  return step * mag;
}
