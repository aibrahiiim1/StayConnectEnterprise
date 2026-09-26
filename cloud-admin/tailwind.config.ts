import type { Config } from "tailwindcss";

// THE TOKEN MAP. Every colour here resolves to a CSS variable defined in app/globals.css, so switching theme
// is a class on <html> and not a second copy of the palette.
//
// `token` exists because Tailwind needs to be able to write `bg-primary/10`. The `<alpha-value>` placeholder is
// Tailwind's own substitution point: it becomes the modifier when one is used and `1` when it is not, which is
// what makes a CSS-variable colour support opacity utilities at all.
const token = (variable: string) => `hsl(var(${variable}) / <alpha-value>)`;

const config: Config = {
  darkMode: "class",
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
    "./lib/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        background: token("--background"),
        foreground: token("--foreground"),

        card: {
          DEFAULT: token("--card"),
          foreground: token("--card-foreground"),
        },
        popover: {
          DEFAULT: token("--popover"),
          foreground: token("--popover-foreground"),
        },
        surface: {
          DEFAULT: token("--surface"),
          foreground: token("--surface-foreground"),
        },
        // `text-muted` IS THE MOST-USED UTILITY ON THIS ADMIN, and it pointed at the wrong token.
        //
        // --muted is a quiet SURFACE (212 22% 95% -- very nearly white) and --muted-foreground is a quiet
        // LABEL (218 13% 44%). globals.css defines both precisely so "a quiet label and a quiet panel" can be
        // styled independently. But Tailwind resolves `text-muted` to muted.DEFAULT, which was the surface --
        // so 517 secondary labels across these screens were rendering near-white text on a white card. That
        // is the illegible grey the operator has been reading around, and it was one mapping, not 517 pages.
        //
        // DEFAULT is therefore the quiet label. The three places that genuinely wanted the quiet panel use
        // `muted-surface` below and say so by name.
        muted: {
          DEFAULT: token("--muted-foreground"),
          foreground: token("--muted-foreground"),
        },
        "muted-surface": token("--muted"),
        accent: {
          DEFAULT: token("--accent"),
          foreground: token("--accent-foreground"),
        },
        primary: {
          DEFAULT: token("--primary"),
          foreground: token("--primary-foreground"),
          hover: token("--primary-hover"),
          press: token("--primary-press"),
          subtle: token("--primary-subtle"),
          "subtle-foreground": token("--primary-subtle-foreground"),
        },
        secondary: {
          DEFAULT: token("--secondary"),
          foreground: token("--secondary-foreground"),
        },
        success: {
          DEFAULT: token("--success"),
          foreground: token("--success-foreground"),
          subtle: token("--success-subtle"),
          "subtle-foreground": token("--success-subtle-foreground"),
        },
        warning: {
          DEFAULT: token("--warning"),
          foreground: token("--warning-foreground"),
          subtle: token("--warning-subtle"),
          "subtle-foreground": token("--warning-subtle-foreground"),
        },
        destructive: {
          DEFAULT: token("--destructive"),
          foreground: token("--destructive-foreground"),
          subtle: token("--destructive-subtle"),
          "subtle-foreground": token("--destructive-subtle-foreground"),
        },
        info: {
          DEFAULT: token("--info"),
          foreground: token("--info-foreground"),
          subtle: token("--info-subtle"),
          "subtle-foreground": token("--info-subtle-foreground"),
        },

        border: {
          DEFAULT: token("--border"),
          strong: token("--border-strong"),
        },
        input: token("--input"),
        ring: token("--ring"),

        sidebar: {
          DEFAULT: token("--sidebar"),
          foreground: token("--sidebar-foreground"),
          muted: token("--sidebar-muted"),
          accent: token("--sidebar-accent"),
          "accent-foreground": token("--sidebar-accent-foreground"),
          border: token("--sidebar-border"),
          active: token("--sidebar-active"),
        },

        chart: {
          1: token("--chart-1"),
          2: token("--chart-2"),
          3: token("--chart-3"),
          4: token("--chart-4"),
          5: token("--chart-5"),
          6: token("--chart-6"),
        },

        // ------------------------------------------------------------------------------------------------
        // COMPATIBILITY ALIASES — the vocabulary the existing forty screens are written in.
        //
        // These are not a second palette. Each one points at the role token above that means the same thing,
        // so a page still using `bg-panel text-muted border-border` renders correctly in BOTH themes without
        // being touched. They stay until the last screen is refitted; new code should use the role names.
        // ------------------------------------------------------------------------------------------------
        bg: token("--background"),
        panel: token("--card"),
        panel2: token("--surface"),
        text: token("--foreground"),
        brand: token("--primary"),
        brandDim: token("--primary-hover"),
        ok: token("--success"),
        warn: token("--warning"),
        err: token("--destructive"),
      },
      // THE ONEGATE RADIUS SCALE, by role (design-system/tokens.css). `md` is every control, `lg` every card
      // and sheet, `xl` every overlay. Screens written against the old scale keep working because the names
      // did not change -- only what they mean became the brand's.
      borderRadius: {
        sm: "5px",
        md: "var(--radius-control)",
        lg: "var(--radius-card)",
        xl: "var(--radius-overlay)",
        control: "var(--radius-control)",
        card: "var(--radius-card)",
        overlay: "var(--radius-overlay)",
      },
      fontFamily: {
        sans: [
          "var(--font-sans)", "ui-sans-serif", "system-ui", "-apple-system", "Segoe UI",
          "Roboto", "Helvetica", "Arial", "sans-serif",
        ],
        mono: [
          "var(--font-mono)", "ui-monospace", "SFMono-Regular", "Menlo", "Monaco",
          "Consolas", "monospace",
        ],
      },
      // THE ONEGATE TYPE SCALE, by role. One family (Inter), ten roles; nothing in between.
      fontSize: {
        "2xs": ["0.6875rem", { lineHeight: "1rem", letterSpacing: "0.02em" }],
        title: ["1.625rem", { lineHeight: "1.3", letterSpacing: "-0.025em", fontWeight: "700" }],
        metric: ["1.5rem", { lineHeight: "1.2", letterSpacing: "-0.02em", fontWeight: "700" }],
        subtitle: ["1.25rem", { lineHeight: "1.4", letterSpacing: "-0.02em", fontWeight: "700" }],
        headline: ["1.125rem", { lineHeight: "1.4", letterSpacing: "-0.015em", fontWeight: "700" }],
        emphasis: ["0.9375rem", { lineHeight: "1.5", fontWeight: "650" }],
        body: ["0.875rem", { lineHeight: "1.45" }],
        label: ["0.8125rem", { lineHeight: "1.45", fontWeight: "600" }],
        caption: ["0.75rem", { lineHeight: "1.45" }],
        micro: ["0.6875rem", { lineHeight: "1.45", fontWeight: "700" }],
        nano: ["0.625rem", { lineHeight: "1.45", fontWeight: "700" }],
      },
      // ELEVATION IS ONLY FOR WHAT FLOATS OR RESPONDS (design-system/tokens.css). Flat lists get none: their
      // depth comes from hairlines and tonal layers, so urgency reads without shadows competing for it.
      boxShadow: {
        xs: "var(--shadow-card)",
        sm: "var(--shadow-card)",
        md: "var(--shadow-card-hover)",
        lg: "var(--shadow-overlay)",
        card: "var(--shadow-card)",
        "card-hover": "var(--shadow-card-hover)",
        overlay: "var(--shadow-overlay)",
        control: "var(--shadow-control)",
        "control-hover": "var(--shadow-control-hover)",
        "control-press": "var(--shadow-control-press)",
        panel: "var(--shadow-card)",
      },
      transitionTimingFunction: { onegate: "var(--motion-ease)" },
      transitionDuration: { press: "80ms", base: "160ms" },
      keyframes: {
        "fade-in": { from: { opacity: "0" }, to: { opacity: "1" } },
        "slide-up": {
          from: { opacity: "0", transform: "translateY(4px)" },
          to: { opacity: "1", transform: "translateY(0)" },
        },
        shimmer: {
          "100%": { transform: "translateX(100%)" },
        },
      },
      animation: {
        "fade-in": "fade-in 160ms ease-out",
        "slide-up": "slide-up 180ms ease-out",
      },
    },
  },
  plugins: [require("tailwindcss-animate")],
};
export default config;
