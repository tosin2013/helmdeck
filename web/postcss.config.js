// PostCSS config — Tailwind v4 ships its plugin via @tailwindcss/postcss
// instead of the v3 `tailwindcss` plugin name. Autoprefixer stays the same.
export default {
  plugins: {
    '@tailwindcss/postcss': {},
    autoprefixer: {},
  },
};
