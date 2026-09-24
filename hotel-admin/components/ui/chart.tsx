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

/**
 * AreaChart — one or more series over TIME, drawn as lines with a faint wash.
 *
 * The column chart is right for a bounded set of bands (24 hours). It is wrong for a week of samples or thirty
 * days: hundreds of 2px columns read as noise. A line states the shape, and the hover crosshair states the exact
 * value at any point. Series are NOT stacked: two traffic directions are compared, not summed, and a stacked area
 * makes the upper series unreadable against a moving baseline.
 */
export function AreaChart({
  data,
  series,
  height = 180,
  formatValue = (n) => n.toLocaleString(),
  formatAxis,
  tickCount = 6,
  className,
  emptyLabel = "No data recorded in this period",
}: {
  data: Point[];
  series: { name: string; token?: SeriesToken }[];
  height?: number;
  formatValue?: (n: number) => string;
  formatAxis?: (n: number) => string;
  /** How many x-axis labels to show, spread evenly. */
  tickCount?: number;
  className?: string;
  emptyLabel?: string;
}) {
  const [hover, setHover] = React.useState<number | null>(null);
  const ref = React.useRef<HTMLDivElement>(null);
  const [w, setW] = React.useState(600);

  React.useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((es) => setW(Math.max(120, es[0].contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const peak = Math.max(0, ...data.flatMap((d) => d.values.map((v) => v || 0)));
  const allZero = peak <= 0;
  const ceiling = allZero ? 1 : niceCeiling(peak);
  const fmtAxis = formatAxis ?? formatValue;
  const n = data.length;
  const pad = 4;
  const x = (i: number) => (n <= 1 ? w / 2 : pad + (i * (w - pad * 2)) / (n - 1));
  const y = (v: number) => height - 2 - ((v || 0) / ceiling) * (height - 8);

  const color = (si: number) => (series[si]?.token ? `hsl(var(--${series[si].token}))` : seriesColor(si));

  const ticks = React.useMemo(() => {
    if (n === 0) return [] as number[];
    const k = Math.max(2, Math.min(tickCount, n));
    return Array.from({ length: k }, (_, j) => Math.round((j * (n - 1)) / (k - 1)));
  }, [n, tickCount]);

  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (n === 0) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const rel = ((e.clientX - rect.left) / rect.width) * w;
    const i = Math.round(((rel - pad) / (w - pad * 2)) * (n - 1));
    setHover(Math.max(0, Math.min(n - 1, i)));
  };

  return (
    <div className={cn("min-w-0", className)}>
      {series.length > 1 && <Legend items={series.map((s, i) => ({ name: s.name, index: i, token: s.token }))} />}
      <div className="flex gap-2">
        <div className="flex w-12 shrink-0 flex-col justify-between text-right" style={{ height }}>
          {[1, 0.5, 0].map((f) => (
            <span key={f} className="text-2xs leading-none tabular text-muted-foreground/70">
              {fmtAxis(ceiling * f)}
            </span>
          ))}
        </div>
        <div ref={ref} className="relative min-w-0 flex-1">
          <svg
            width="100%"
            height={height}
            viewBox={`0 0 ${w} ${height}`}
            preserveAspectRatio="none"
            className="block overflow-visible"
            onMouseMove={onMove}
            onMouseLeave={() => setHover(null)}
            role="img"
            aria-label={`${series.map((s) => s.name).join(" and ")} over time`}
          >
            {[0, 0.5, 1].map((f) => (
              <line
                key={f}
                x1={0}
                x2={w}
                y1={2 + f * (height - 8)}
                y2={2 + f * (height - 8)}
                stroke="hsl(var(--border))"
                strokeWidth={1}
                vectorEffect="non-scaling-stroke"
              />
            ))}
            {!allZero &&
              series.map((_, si) => {
                const pts = data.map((d, i) => `${x(i).toFixed(1)},${y(d.values[si] ?? 0).toFixed(1)}`);
                if (pts.length === 0) return null;
                const line = `M${pts.join(" L")}`;
                const area = `${line} L${x(n - 1).toFixed(1)},${height} L${x(0).toFixed(1)},${height} Z`;
                return (
                  <g key={si}>
                    <path d={area} fill={color(si)} opacity={0.08} />
                    <path
                      d={line}
                      fill="none"
                      stroke={color(si)}
                      strokeWidth={2}
                      strokeLinejoin="round"
                      strokeLinecap="round"
                      vectorEffect="non-scaling-stroke"
                    />
                  </g>
                );
              })}
            {hover !== null && (
              <line
                x1={x(hover)}
                x2={x(hover)}
                y1={0}
                y2={height}
                stroke="hsl(var(--border-strong))"
                strokeWidth={1}
                vectorEffect="non-scaling-stroke"
              />
            )}
          </svg>
          {hover !== null && !allZero && data[hover] && (
            <div
              className="pointer-events-none absolute top-0 z-20 -translate-x-1/2 rounded-md border border-border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md"
              style={{ left: `${(x(hover) / w) * 100}%` }}
            >
              <div className="mb-1 text-2xs font-semibold uppercase tracking-wide text-muted-foreground">
                {data[hover].label}
              </div>
              <div className="space-y-0.5">
                {series.map((s, si) => (
                  <Row key={si} swatch={color(si)} name={s.name} value={formatValue(data[hover].values[si] ?? 0)} />
                ))}
              </div>
            </div>
          )}
          <div className="relative mt-1.5 h-4">
            {ticks.map((i) => (
              <span
                key={i}
                className="absolute -translate-x-1/2 whitespace-nowrap text-2xs tabular text-muted-foreground/70"
                style={{ left: `${(x(i) / w) * 100}%` }}
              >
                {data[i]?.label}
              </span>
            ))}
          </div>
        </div>
      </div>
      {allZero && <p className="mt-2 text-center text-xs text-muted-foreground">{emptyLabel}</p>}
    </div>
  );
}

/**
 * BarList — a ranked list of named quantities ("top packages", "sign-in outcomes").
 *
 * The bar sits BEHIND the label, so a long name never fights a separate axis for space, and the value is always
 * printed: the bar shows proportion, the number states it.
 */
export function BarList({
  items,
  formatValue = (n) => n.toLocaleString(),
  max,
  className,
  emptyLabel = "Nothing recorded yet",
}: {
  items: {
    name: React.ReactNode;
    value: number;
    key?: string;
    tone?: "ok" | "warn" | "err" | "info" | "neutral";
    hint?: React.ReactNode;
  }[];
  formatValue?: (n: number) => string;
  max?: number;
  className?: string;
  emptyLabel?: string;
}) {
  const top = max ?? Math.max(0, ...items.map((i) => i.value));
  const toneBg: Record<string, string> = {
    ok: "bg-success/15",
    warn: "bg-warning/20",
    err: "bg-destructive/15",
    info: "bg-info/15",
    neutral: "bg-muted-foreground/10",
  };
  if (items.length === 0) return <p className={cn("text-sm text-muted-foreground", className)}>{emptyLabel}</p>;
  return (
    <ul className={cn("space-y-1.5", className)}>
      {items.map((it, i) => {
        const pct = top > 0 ? Math.max(1.5, (it.value / top) * 100) : 0;
        return (
          <li key={it.key ?? i} className="flex items-center gap-3">
            <div className="relative min-w-0 flex-1 overflow-hidden rounded-md">
              <span
                className={cn("absolute inset-y-0 left-0 rounded-md", it.tone ? toneBg[it.tone] : "bg-primary/10")}
                style={{ width: `${pct}%` }}
                aria-hidden
              />
              <div className="relative flex min-w-0 items-baseline gap-2 px-2.5 py-1.5 text-sm">
                <span className="truncate">{it.name}</span>
                {it.hint && <span className="truncate text-xs text-muted-foreground">{it.hint}</span>}
              </div>
            </div>
            <span className="w-20 shrink-0 text-right text-sm font-medium tabular">{formatValue(it.value)}</span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * Heatmap — counts on a day × hour grid ("when do guests sign in?").
 *
 * A cell's shade is its share of the busiest cell. Every cell carries its count as an accessible label and a
 * tooltip, so the grid is readable by value rather than only by colour.
 */
export function Heatmap({
  rows,
  columns,
  values,
  formatValue = (n) => n.toLocaleString(),
  className,
}: {
  rows: string[];
  columns: string[];
  /** values[rowIndex][columnIndex] */
  values: number[][];
  formatValue?: (n: number) => string;
  className?: string;
}) {
  const peak = Math.max(0, ...values.flat());
  return (
    <div className={cn("min-w-0 overflow-x-auto", className)}>
      <table className="border-separate border-spacing-[3px]">
        <tbody>
          {rows.map((r, ri) => (
            <tr key={r}>
              <th scope="row" className="pr-2 text-right text-2xs font-medium text-muted-foreground">
                {r}
              </th>
              {columns.map((c, ci) => {
                const v = values[ri]?.[ci] ?? 0;
                const a = peak > 0 ? v / peak : 0;
                return (
                  <td
                    key={c}
                    title={`${r} ${c}: ${formatValue(v)}`}
                    aria-label={`${r} ${c}: ${formatValue(v)}`}
                    className="size-4 min-w-4 rounded-[3px] bg-surface"
                    style={v > 0 ? { background: `hsl(var(--chart-1) / ${(0.12 + a * 0.88).toFixed(2)})` } : undefined}
                  />
                );
              })}
            </tr>
          ))}
          <tr>
            <td />
            {columns.map((c, ci) => (
              <td key={c} className="text-center text-2xs tabular text-muted-foreground/70">
                {ci % 3 === 0 ? c : ""}
              </td>
            ))}
          </tr>
        </tbody>
      </table>
    </div>
  );
}
