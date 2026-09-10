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
        muted: {
          DEFAULT: token("--muted"),
          foreground: token("--muted-foreground"),
        },
        accent: {
          DEFAULT: token("--accent"),
          foreground: token("--accent-foreground"),
        },
        primary: {
          DEFAULT: token("--primary"),
          foreground: token("--primary-foreground"),
          hover: token("--primary-hover"),
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
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
        xl: "calc(var(--radius) + 4px)",
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
      fontSize: {
        // A label scale the product did not have. Screens reached for text-[10px] and text-[11px] inline,
        // which is how six different "small" sizes ended up on one page.
        "2xs": ["0.6875rem", { lineHeight: "1rem", letterSpacing: "0.02em" }],
      },
      boxShadow: {
        // Elevation is deliberately restrained: an admin surface that floats everywhere reads as unfinished.
        xs: "0 1px 2px 0 hsl(222 24% 13% / 0.04)",
        sm: "0 1px 3px 0 hsl(222 24% 13% / 0.06), 0 1px 2px -1px hsl(222 24% 13% / 0.06)",
        md: "0 4px 12px -2px hsl(222 24% 13% / 0.08), 0 2px 4px -2px hsl(222 24% 13% / 0.06)",
        lg: "0 12px 32px -8px hsl(222 24% 13% / 0.16), 0 4px 10px -4px hsl(222 24% 13% / 0.08)",
        // Retained so `shadow-panel` in existing pages stays valid.
        panel: "0 1px 2px 0 hsl(222 24% 13% / 0.04)",
      },
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
