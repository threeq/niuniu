/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ["class"],
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      fontFamily: {
        sans: ['"Noto Sans SC"', 'system-ui', '-apple-system', 'sans-serif'],
      },
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        popover: {
          DEFAULT: "hsl(var(--popover))",
          foreground: "hsl(var(--popover-foreground))",
        },
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
        surface: {
          DEFAULT: "hsl(var(--surface))",
          muted: "hsl(var(--surface-muted))",
          raised: "hsl(var(--surface-raised))",
        },
        success: {
          DEFAULT: "hsl(var(--success))",
          foreground: "hsl(var(--success-foreground))",
        },
        warning: {
          DEFAULT: "hsl(var(--warning))",
          foreground: "hsl(var(--warning-foreground))",
        },
        info: {
          DEFAULT: "hsl(var(--info))",
          foreground: "hsl(var(--info-foreground))",
        },
        diff: {
          add: "hsl(var(--diff-add-bg))",
          del: "hsl(var(--diff-del-bg))",
          "add-fg": "hsl(var(--diff-add-fg))",
          "del-fg": "hsl(var(--diff-del-fg))",
        },
        syntax: {
          keyword: "hsl(var(--syntax-keyword))",
          string:  "hsl(var(--syntax-string))",
          comment: "hsl(var(--syntax-comment))",
          number:  "hsl(var(--syntax-number))",
        },
        // Design system tokens — see docs/design-system.md
        warm: {
          canvas:        "hsl(var(--warm-canvas))",
          surface:       "hsl(var(--warm-surface))",
          muted:         "hsl(var(--warm-muted))",
          border:        "hsl(var(--warm-border))",
          text:          "hsl(var(--warm-text))",
          "text-muted":  "hsl(var(--warm-text-muted))",
        },
        brand: {
          DEFAULT:    "hsl(var(--brand))",
          foreground: "hsl(var(--brand-foreground))",
          soft:       "hsl(var(--brand-soft))",
        },
        prio: {
          low:      "hsl(var(--prio-low))",
          medium:   "hsl(var(--prio-medium))",
          high:     "hsl(var(--prio-high))",
          critical: "hsl(var(--prio-critical))",
        },
        col: {
          backlog:      "hsl(var(--col-backlog))",
          spec:         "hsl(var(--col-spec))",
          impl:         "hsl(var(--col-impl))",
          review:       "hsl(var(--col-review))",
          done:         "hsl(var(--col-done))",
          "in-progress": "hsl(var(--col-in-progress))",
        },
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
      keyframes: {
        "accordion-down": {
          from: { height: "0" },
          to: { height: "var(--radix-accordion-content-height)" },
        },
        "accordion-up": {
          from: { height: "var(--radix-accordion-content-height)" },
          to: { height: "0" },
        },
      },
      animation: {
        "accordion-down": "accordion-down 0.2s ease-out",
        "accordion-up": "accordion-up 0.2s ease-out",
      },
    },
  },
  plugins: [require("@tailwindcss/container-queries"), require("@tailwindcss/typography")],
}
