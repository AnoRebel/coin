export default defineAppConfig({
  docus: { locale: "en" },

  header: { title: "AnoRebelCoin" },

  seo: {
    title: "AnoRebelCoin documentation",
    description:
      "Using, running and building on AnoRebelCoin (ARCE) and AnoRebelNetwork (ARNE).",
  },

  github: {
    url: "https://github.com/AnoRebel/coin",
    branch: "main",
    rootDir: "docs",
  },

  toc: {
    bottom: {
      title: "For LLMs",
      links: [
        { icon: "i-lucide-file-text", label: "llms.txt", to: "/llms.txt", target: "_blank" },
        { icon: "i-lucide-files", label: "llms-full.txt", to: "/llms-full.txt", target: "_blank" },
      ],
    },
  },

  ui: {
    colors: {
      primary: "emerald",
      neutral: "zinc",
    },
  },
});
