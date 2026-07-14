import type { Config } from "tailwindcss";

const config: Config = {
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        bg: "#0b0d12",
        panel: "#14171f",
        panel2: "#1a1e28",
        border: "#262b37",
        muted: "#8a93a8",
        text: "#e6e9f0",
        brand: "#5b8cff",
        brandDim: "#3a69d1",
        ok: "#38d17a",
        warn: "#f5b342",
        err: "#ff5c66",
      },
      fontFamily: {
        sans: ["ui-sans-serif", "system-ui", "-apple-system", "Segoe UI", "Roboto", "Helvetica", "Arial", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "Monaco", "Consolas", "monospace"],
      },
      boxShadow: {
        panel: "0 1px 0 0 rgb(255 255 255 / 0.02), 0 0 0 1px rgb(255 255 255 / 0.03)",
      },
    },
  },
  plugins: [],
};
export default config;
