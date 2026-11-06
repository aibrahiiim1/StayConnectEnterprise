"use client";

import { useId, useMemo } from "react";
import { cn } from "@/lib/utils";

const GATE_GREEN = "#15803D";
const WHITE = "#FFFFFF";

const WORDMARK_VIEWBOX = "34 -716 4068 727";
const WORDMARK_ONE = [
  "M403.0 11Q325.0 11 257.5 -17.0Q190.0 -45 140.0 -94.5Q90.0 -144 62.0 -210.5Q34.0 -277 34.0 -353Q34.0 -430 61.5 -496.0Q89.0 -562 139.0 -611.5Q189.0 -661 256.0 -688.5Q323.0 -716 401.0 -716Q479.0 -716 546.0 -688.5Q613.0 -661 663.0 -611.5Q713.0 -562 741.0 -495.5Q769.0 -429 769.0 -352Q769.0 -276 741.0 -209.5Q713.0 -143 663.0 -93.5Q613.0 -44 546.5 -16.5Q480.0 11 403.0 11ZM401.0 -123Q466.0 -123 515.0 -152.0Q564.0 -181 591.5 -233.0Q619.0 -285 619.0 -353Q619.0 -404 603.0 -446.0Q587.0 -488 558.0 -518.5Q529.0 -549 489.5 -565.0Q450.0 -581 401.0 -581Q336.0 -581 287.5 -552.5Q239.0 -524 211.5 -473.0Q184.0 -422 184.0 -353Q184.0 -302 199.5 -259.5Q215.0 -217 244.0 -187.0Q273.0 -157 313.0 -140.0Q353.0 -123 401.0 -123Z",
  "M1167.0 0V-277Q1167.0 -318 1141.5 -343.0Q1116.0 -368 1077.0 -368Q1050.0 -368 1029.5 -356.5Q1009.0 -345 997.5 -324.5Q986.0 -304 986.0 -277V0H844.0V-485H986.0V-443Q1003.0 -459 1024.0 -471Q1068.0 -495 1124.0 -495Q1177.0 -495 1219.0 -469.0Q1261.0 -443 1285.0 -400.5Q1309.0 -358 1309.0 -309V0Z",
  "M1631.0 11Q1554.0 11 1493.5 -21.5Q1433.0 -54 1398.5 -111.5Q1364.0 -169 1364.0 -242Q1364.0 -315 1398.0 -372.0Q1432.0 -429 1490.5 -462.0Q1549.0 -495 1621.0 -495Q1692.0 -495 1746.5 -464.0Q1801.0 -433 1832.0 -378.0Q1863.0 -323 1863.0 -253Q1863.0 -240 1861.5 -226.0Q1860.0 -212 1856.0 -195L1505.0 -194Q1509.0 -180 1515.0 -169Q1531.0 -137 1560.5 -120.5Q1590.0 -104 1631.0 -104Q1667.0 -104 1696.5 -116.0Q1726.0 -128 1748.0 -153L1829.0 -73Q1793.0 -31 1742.0 -10.0Q1691.0 11 1631.0 11ZM1505.0 -295 1728.0 -296Q1725.0 -311 1719.0 -324Q1706.0 -353 1682.0 -367.5Q1658.0 -382 1622.0 -382Q1584.0 -382 1556.5 -365.5Q1529.0 -349 1514.0 -318Q1509.0 -307 1505.0 -295Z",
];
const WORDMARK_GATE = [
  "M2268.0 11Q2192.0 11 2125.5 -17.0Q2059.0 -45 2009.0 -94.5Q1959.0 -144 1931.0 -210.0Q1903.0 -276 1903.0 -353Q1903.0 -430 1932.0 -495.5Q1961.0 -561 2011.5 -610.5Q2062.0 -660 2130.0 -687.5Q2198.0 -715 2276.0 -715Q2365.0 -715 2439.0 -682.5Q2513.0 -650 2561.0 -591L2462.0 -492Q2429.0 -535 2381.5 -558.0Q2334.0 -581 2275.0 -581Q2210.0 -581 2160.0 -552.5Q2110.0 -524 2081.5 -472.5Q2053.0 -421 2053.0 -353.0Q2053.0 -285 2081.0 -233.5Q2109.0 -182 2157.0 -153.0Q2205.0 -124 2268.0 -124Q2332.0 -124 2377.5 -148.0Q2423.0 -172 2447.0 -218Q2458.0 -240 2464.0 -266L2256.0 -269V-394H2621.0V-373Q2621.0 -247 2575.5 -161.5Q2530.0 -76 2450.5 -32.5Q2371.0 11 2268.0 11Z",
  "M2893.0 10Q2827.0 10 2774.0 -23.0Q2721.0 -56 2691.0 -113.0Q2661.0 -170 2661.0 -242Q2661.0 -314 2691.0 -371.0Q2721.0 -428 2774.0 -461.5Q2827.0 -495 2893.0 -495Q2944.0 -495 2984.0 -474Q3010.0 -461 3030.0 -441V-485H3171.0V0H3030.0V-43Q3011.0 -23 2984.0 -10Q2944.0 10 2893.0 10ZM2921.0 -120Q2973.0 -120 3005.0 -154.0Q3037.0 -188 3037.0 -242Q3037.0 -279 3022.5 -306.5Q3008.0 -334 2982.0 -349.5Q2956.0 -365 2921.0 -365Q2887.0 -365 2861.0 -349.5Q2835.0 -334 2820.0 -306.0Q2805.0 -278 2805.0 -242.0Q2805.0 -206 2820.0 -178.5Q2835.0 -151 2861.0 -135.5Q2887.0 -120 2921.0 -120Z",
  "M3337.0 0V-362H3226.0V-485H3337.0V-685H3479.0V-485H3590.0V-362H3479.0V0Z",
  "M3870.0 11Q3793.0 11 3732.5 -21.5Q3672.0 -54 3637.5 -111.5Q3603.0 -169 3603.0 -242Q3603.0 -315 3637.0 -372.0Q3671.0 -429 3729.5 -462.0Q3788.0 -495 3860.0 -495Q3931.0 -495 3985.5 -464.0Q4040.0 -433 4071.0 -378.0Q4102.0 -323 4102.0 -253Q4102.0 -240 4100.5 -226.0Q4099.0 -212 4095.0 -195L3744.0 -194Q3748.0 -180 3754.0 -169Q3770.0 -137 3799.5 -120.5Q3829.0 -104 3870.0 -104Q3906.0 -104 3935.5 -116.0Q3965.0 -128 3987.0 -153L4068.0 -73Q4032.0 -31 3981.0 -10.0Q3930.0 11 3870.0 11ZM3744.0 -295 3967.0 -296Q3964.0 -311 3958.0 -324Q3945.0 -353 3921.0 -367.5Q3897.0 -382 3861.0 -382Q3823.0 -382 3795.5 -365.5Q3768.0 -349 3753.0 -318Q3748.0 -307 3744.0 -295Z",
];

const MARK_VIEWBOX = "0 0 1303.6 1303.6";
const MARK_O_TRANSFORM = "translate(248.8 1004.8)";
const MARK_O = "M405.0 12Q325.0 12 257.5 -16.0Q190.0 -44 139.0 -94.0Q88.0 -144 60.0 -210.5Q32.0 -277 32.0 -354Q32.0 -432 60.0 -498.0Q88.0 -564 138.0 -613.5Q188.0 -663 255.5 -690.5Q323.0 -718 403.0 -718Q482.0 -718 549.5 -690.5Q617.0 -663 667.5 -613.5Q718.0 -564 746.0 -497.5Q774.0 -431 774.0 -353Q774.0 -276 746.0 -209.5Q718.0 -143 668.0 -93.5Q618.0 -44 550.5 -16.0Q483.0 12 405.0 12ZM403.0 -131Q466.0 -131 513.5 -159.0Q561.0 -187 587.0 -237.5Q613.0 -288 613.0 -354Q613.0 -404 598.0 -444.5Q583.0 -485 555.0 -514.5Q527.0 -544 488.5 -559.5Q450.0 -575 403.0 -575Q340.0 -575 292.5 -547.5Q245.0 -520 219.0 -470.5Q193.0 -421 193.0 -354Q193.0 -304 208.0 -263.0Q223.0 -222 250.5 -192.5Q278.0 -163 317.0 -147.0Q356.0 -131 403.0 -131Z";

const ANIMATED_WORDMARK = "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"-9.0 -759.0 4153.9 812.9\" role=\"img\" aria-label=\"OneGate\"><title>OneGate</title><style>.LOGOID-lead,.LOGOID-push,.LOGOID-slide,.LOGOID-wipe,.LOGOID-l,.LOGOID-dot{animation-duration:2.4s;animation-timing-function:linear;animation-iteration-count:1;animation-fill-mode:both}.LOGOID-lead{animation-name:LOGOID-pop;transform-box:fill-box;transform-origin:50% 50%}.LOGOID-push{animation-name:LOGOID-push;transform-box:fill-box;transform-origin:50% 50%}.LOGOID-slide{animation-name:LOGOID-slide}.LOGOID-wipe{animation-name:LOGOID-wipe}.LOGOID-dot{animation-name:LOGOID-drop}.LOGOID-hov{transition:transform .5s cubic-bezier(.34,1.56,.64,1)}svg:hover .LOGOID-hov{transform:translateX(50px)}@keyframes LOGOID-pop{0.000%{opacity:0;transform:scale(.72);animation-timing-function:cubic-bezier(.34,1.56,.64,1)}22.917%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-push{0.000%{transform:none}68.750%{transform:none;animation-timing-function:cubic-bezier(.3,0,.3,1)}77.083%{transform:scale(1.07);animation-timing-function:cubic-bezier(.3,0,.3,1)}89.583%{transform:none}100.000%{transform:none}}@keyframes LOGOID-slide{0.000%{transform:translateX(1666px)}29.167%{transform:translateX(1666px);animation-timing-function:cubic-bezier(0.7,0,0.25,1)}70.833%{transform:none}100.000%{transform:none}}@keyframes LOGOID-wipe{0.000%{transform:none}29.167%{transform:none;animation-timing-function:cubic-bezier(0.7,0,0.25,1)}70.833%{transform:translateX(3605px)}100.000%{transform:translateX(3605px)}}@keyframes LOGOID-l0{0.000%{opacity:0;transform:translateY(72px)}36.444%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}55.194%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-l1{0.000%{opacity:0;transform:translateY(72px)}41.260%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}60.010%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-l2{0.000%{opacity:0;transform:translateY(72px)}44.008%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}62.758%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-l3{0.000%{opacity:0;transform:translateY(72px)}46.141%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}64.891%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-l4{0.000%{opacity:0;transform:translateY(72px)}48.212%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}66.962%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@keyframes LOGOID-l5{0.000%{opacity:0;transform:translateY(72px)}51.331%{opacity:0;transform:translateY(72px);animation-timing-function:cubic-bezier(.2,.8,.2,1)}70.081%{opacity:1;transform:none}100.000%{opacity:1;transform:none}}@media (prefers-reduced-motion:reduce){[class^=\"LOGOID-\"]{animation:none!important}.LOGOID-wipe{transform:translateX(3605px)}}</style><defs><linearGradient id=\"LOGOID-f\" x1=\"0\" x2=\"1\"><stop offset=\"0\" stop-color=\"#fff\"/><stop offset=\"0.9564\" stop-color=\"#fff\"/><stop offset=\"1\" stop-color=\"#fff\" stop-opacity=\"0\"/></linearGradient><mask id=\"LOGOID-m\" maskUnits=\"userSpaceOnUse\" x=\"-682\" y=\"-1432\" width=\"6932\" height=\"2159\"><rect class=\"LOGOID-wipe\" x=\"-4970\" y=\"-1432\" width=\"5753\" height=\"2159\" fill=\"url(#LOGOID-f)\"/></mask></defs><g class=\"LOGOID-root\"><g class=\"LOGOID-slide\"><g class=\"LOGOID-lead\"><g class=\"LOGOID-push\"><path d=\"M403.0 11Q325.0 11 257.5 -17.0Q190.0 -45 140.0 -94.5Q90.0 -144 62.0 -210.5Q34.0 -277 34.0 -353Q34.0 -430 61.5 -496.0Q89.0 -562 139.0 -611.5Q189.0 -661 256.0 -688.5Q323.0 -716 401.0 -716Q479.0 -716 546.0 -688.5Q613.0 -661 663.0 -611.5Q713.0 -562 741.0 -495.5Q769.0 -429 769.0 -352Q769.0 -276 741.0 -209.5Q713.0 -143 663.0 -93.5Q613.0 -44 546.5 -16.5Q480.0 11 403.0 11ZM401.0 -123Q466.0 -123 515.0 -152.0Q564.0 -181 591.5 -233.0Q619.0 -285 619.0 -353Q619.0 -404 603.0 -446.0Q587.0 -488 558.0 -518.5Q529.0 -549 489.5 -565.0Q450.0 -581 401.0 -581Q336.0 -581 287.5 -552.5Q239.0 -524 211.5 -473.0Q184.0 -422 184.0 -353Q184.0 -302 199.5 -259.5Q215.0 -217 244.0 -187.0Q273.0 -157 313.0 -140.0Q353.0 -123 401.0 -123Z\" fill=\"var(--logo-accent,#15803D)\"/></g></g><g class=\"LOGOID-hov\"><g mask=\"url(#LOGOID-m)\"><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l0\" d=\"M1167.0 0V-277Q1167.0 -318 1141.5 -343.0Q1116.0 -368 1077.0 -368Q1050.0 -368 1029.5 -356.5Q1009.0 -345 997.5 -324.5Q986.0 -304 986.0 -277V0H844.0V-485H986.0V-443Q1003.0 -459 1024.0 -471Q1068.0 -495 1124.0 -495Q1177.0 -495 1219.0 -469.0Q1261.0 -443 1285.0 -400.5Q1309.0 -358 1309.0 -309V0Z\" fill=\"var(--logo-accent,#15803D)\"/><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l1\" d=\"M1631.0 11Q1554.0 11 1493.5 -21.5Q1433.0 -54 1398.5 -111.5Q1364.0 -169 1364.0 -242Q1364.0 -315 1398.0 -372.0Q1432.0 -429 1490.5 -462.0Q1549.0 -495 1621.0 -495Q1692.0 -495 1746.5 -464.0Q1801.0 -433 1832.0 -378.0Q1863.0 -323 1863.0 -253Q1863.0 -240 1861.5 -226.0Q1860.0 -212 1856.0 -195L1505.0 -194Q1509.0 -180 1515.0 -169Q1531.0 -137 1560.5 -120.5Q1590.0 -104 1631.0 -104Q1667.0 -104 1696.5 -116.0Q1726.0 -128 1748.0 -153L1829.0 -73Q1793.0 -31 1742.0 -10.0Q1691.0 11 1631.0 11ZM1505.0 -295 1728.0 -296Q1725.0 -311 1719.0 -324Q1706.0 -353 1682.0 -367.5Q1658.0 -382 1622.0 -382Q1584.0 -382 1556.5 -365.5Q1529.0 -349 1514.0 -318Q1509.0 -307 1505.0 -295Z\" fill=\"var(--logo-accent,#15803D)\"/><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l2\" d=\"M2268.0 11Q2192.0 11 2125.5 -17.0Q2059.0 -45 2009.0 -94.5Q1959.0 -144 1931.0 -210.0Q1903.0 -276 1903.0 -353Q1903.0 -430 1932.0 -495.5Q1961.0 -561 2011.5 -610.5Q2062.0 -660 2130.0 -687.5Q2198.0 -715 2276.0 -715Q2365.0 -715 2439.0 -682.5Q2513.0 -650 2561.0 -591L2462.0 -492Q2429.0 -535 2381.5 -558.0Q2334.0 -581 2275.0 -581Q2210.0 -581 2160.0 -552.5Q2110.0 -524 2081.5 -472.5Q2053.0 -421 2053.0 -353.0Q2053.0 -285 2081.0 -233.5Q2109.0 -182 2157.0 -153.0Q2205.0 -124 2268.0 -124Q2332.0 -124 2377.5 -148.0Q2423.0 -172 2447.0 -218Q2458.0 -240 2464.0 -266L2256.0 -269V-394H2621.0V-373Q2621.0 -247 2575.5 -161.5Q2530.0 -76 2450.5 -32.5Q2371.0 11 2268.0 11Z\" fill=\"currentColor\"/><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l3\" d=\"M2893.0 10Q2827.0 10 2774.0 -23.0Q2721.0 -56 2691.0 -113.0Q2661.0 -170 2661.0 -242Q2661.0 -314 2691.0 -371.0Q2721.0 -428 2774.0 -461.5Q2827.0 -495 2893.0 -495Q2944.0 -495 2984.0 -474Q3010.0 -461 3030.0 -441V-485H3171.0V0H3030.0V-43Q3011.0 -23 2984.0 -10Q2944.0 10 2893.0 10ZM2921.0 -120Q2973.0 -120 3005.0 -154.0Q3037.0 -188 3037.0 -242Q3037.0 -279 3022.5 -306.5Q3008.0 -334 2982.0 -349.5Q2956.0 -365 2921.0 -365Q2887.0 -365 2861.0 -349.5Q2835.0 -334 2820.0 -306.0Q2805.0 -278 2805.0 -242.0Q2805.0 -206 2820.0 -178.5Q2835.0 -151 2861.0 -135.5Q2887.0 -120 2921.0 -120Z\" fill=\"currentColor\"/><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l4\" d=\"M3337.0 0V-362H3226.0V-485H3337.0V-685H3479.0V-485H3590.0V-362H3479.0V0Z\" fill=\"currentColor\"/><path class=\"LOGOID-l\" style=\"animation-name:LOGOID-l5\" d=\"M3870.0 11Q3793.0 11 3732.5 -21.5Q3672.0 -54 3637.5 -111.5Q3603.0 -169 3603.0 -242Q3603.0 -315 3637.0 -372.0Q3671.0 -429 3729.5 -462.0Q3788.0 -495 3860.0 -495Q3931.0 -495 3985.5 -464.0Q4040.0 -433 4071.0 -378.0Q4102.0 -323 4102.0 -253Q4102.0 -240 4100.5 -226.0Q4099.0 -212 4095.0 -195L3744.0 -194Q3748.0 -180 3754.0 -169Q3770.0 -137 3799.5 -120.5Q3829.0 -104 3870.0 -104Q3906.0 -104 3935.5 -116.0Q3965.0 -128 3987.0 -153L4068.0 -73Q4032.0 -31 3981.0 -10.0Q3930.0 11 3870.0 11ZM3744.0 -295 3967.0 -296Q3964.0 -311 3958.0 -324Q3945.0 -353 3921.0 -367.5Q3897.0 -382 3861.0 -382Q3823.0 -382 3795.5 -365.5Q3768.0 -349 3753.0 -318Q3748.0 -307 3744.0 -295Z\" fill=\"currentColor\"/></g></g></g></g></svg>";

const WORDMARK_COLOURS = "[--og-one:#15803D] [--og-gate:#18181B] dark:[--og-one:#4ADE80] dark:[--og-gate:#FFFFFF]";

export function OneGateWordmark({ className, animated = false }: { className?: string; animated?: boolean }) {
  const uid = "og" + useId().replace(/[^a-zA-Z0-9]/g, "");
  const html = useMemo(
    () =>
      animated
        ? ANIMATED_WORDMARK.replaceAll("LOGOID", uid)
            .replace("<svg ", '<svg style="height:100%;width:auto;display:block;overflow:visible" ')
            .replaceAll("var(--logo-accent,#15803D)", "var(--og-one)")
            .replaceAll('fill="currentColor"', 'fill="var(--og-gate)"')
        : "",
    [uid, animated],
  );
  if (animated) {
    return (
      <span
        role="img"
        aria-label="OneGate"
        className={cn("inline-block h-6 leading-none", WORDMARK_COLOURS, className)}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  }
  return (
    <svg
      viewBox={WORDMARK_VIEWBOX}
      preserveAspectRatio="xMinYMid meet"
      role="img"
      aria-label="OneGate"
      className={cn("h-6 w-auto shrink-0", WORDMARK_COLOURS, className)}
    >
      {WORDMARK_ONE.map((d) => <path key={d} d={d} fill="var(--og-one)" />)}
      {WORDMARK_GATE.map((d) => <path key={d} d={d} fill="var(--og-gate)" />)}
    </svg>
  );
}

export function OneGateMark({ className, title }: { className?: string; title?: string }) {
  return (
    <svg
      viewBox={MARK_VIEWBOX}
      className={cn("size-5 shrink-0", className)}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <rect width="1303.6" height="1303.6" rx="260.7" fill={GATE_GREEN} />
      <path transform={MARK_O_TRANSFORM} d={MARK_O} fill={WHITE} />
    </svg>
  );
}

export function OneGateLockup({
  product,
  collapsed = false,
  inverse = false,
  animated = false,
  className,
}: {
  product?: string;
  collapsed?: boolean;
  inverse?: boolean;
  animated?: boolean;
  className?: string;
}) {
  if (collapsed) {
    return <OneGateMark className={cn("size-9", className)} title="OneGate" />;
  }
  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      <OneGateWordmark className="h-[1.375rem] self-start" animated={animated} />
      {product ? (
        <div
          className={cn(
            "min-w-0 truncate text-nano uppercase leading-tight tracking-[0.12em]",
            inverse ? "text-sidebar-muted" : "text-muted-foreground",
          )}
        >
          {product}
        </div>
      ) : null}
    </div>
  );
}

export function BySemantics({ className }: { className?: string }) {
  return <span className={cn("text-caption", className)}>OneGate by Semantics</span>;
}

type LoaderTone = "brand" | "current" | "white";

const LOADER_STROKE: Record<LoaderTone, string> = { brand: GATE_GREEN, current: "currentColor", white: WHITE };

const LOADER_CSS =
  ".og-ld-a{transform-origin:652px 652px;animation:og-ld-r 1.1s linear infinite}" +
  "@keyframes og-ld-r{to{transform:rotate(360deg)}}" +
  ".og-ld-a circle{animation:og-ld-d 2.2s ease-in-out infinite}" +
  "@keyframes og-ld-d{0%{stroke-dasharray:236 2949}50%{stroke-dasharray:1769 2949}100%{stroke-dasharray:236 2949;stroke-dashoffset:-2949}}" +
  "@media (prefers-reduced-motion:reduce){.og-ld-a,.og-ld-a circle{animation:none}}";

export function OneGateLoader({
  className,
  tone = "brand",
  appIcon = false,
  label,
}: {
  className?: string;
  tone?: LoaderTone;
  appIcon?: boolean;
  label?: string;
}) {
  const stroke = appIcon ? WHITE : LOADER_STROKE[tone];
  return (
    <svg
      viewBox="0 0 1304 1304"
      className={cn("size-4 shrink-0", className)}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <style>{LOADER_CSS}</style>
      {appIcon && <rect width="1304" height="1304" rx="261" fill={GATE_GREEN} />}
      <circle cx="652" cy="652" r="469" fill="none" stroke={stroke} strokeOpacity=".2" strokeWidth="169" />
      <g className="og-ld-a">
        <circle
          cx="652"
          cy="652"
          r="469"
          fill="none"
          stroke={stroke}
          strokeWidth="169"
          strokeLinecap="round"
          transform="rotate(-90 652 652)"
        />
      </g>
    </svg>
  );
}
