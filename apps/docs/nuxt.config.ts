export default defineNuxtConfig({
  extends: ["docus"],

  compatibilityDate: "2026-10-01",

  // Nuxt 5 behaviour on Nuxt 4. Docus and @nuxt/content support it.
  future: { compatibilityVersion: 5 },

  experimental: {
    // Docus's nuxt-llms imports useNitroApp from #imports, which v5 no longer
    // auto-imports.
    nitroAutoImports: true,
  },

  // Node's built-in SQLite (Node 22.13+), so the image needs no native build.
  content: { experimental: { sqliteConnector: "native" } },

  site: {
    name: "AnoRebelCoin",
    url: "https://coin.anorebel.net",
  },

  llms: {
    domain: "https://coin.anorebel.net",
    title: "AnoRebelCoin documentation",
    description:
      "Using, running and building on AnoRebelCoin (ARCE) and AnoRebelNetwork (ARNE).",
    full: {
      title: "AnoRebelCoin: full documentation",
      description: "Every AnoRebelCoin documentation page in one Markdown file.",
    },
    notes: [
      "Reading this documentation as an agent: append `.md` to any page URL, or send `Accept: text/markdown`.",
    ],
  },

  // Icons named only in app.config strings aren't found by the scan; bundle them.
  icon: {
    clientBundle: {
      icons: ["lucide:wallet", "lucide:code", "lucide:server", "lucide:files", "lucide:file-text"],
    },
  },

  // The layer draws social preview images while rendering; this site runs as a
  // server, so the renderer has no native dependency to satisfy.
  ogImage: { enabled: false },
});
