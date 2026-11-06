export default defineAppConfig({
  docus: { locale: "en" },

  header: { title: "AnoRebelCoin" },

  seo: {
    titleTemplate: "%s - AnoRebelCoin",
    title: "AnoRebelCoin documentation",
    description:
      "Using, running and building on AnoRebelCoin (ARCE) and AnoRebelNetwork (ARNE).",
  },

  github: {
    owner: "AnoRebel",
    name: "coin",
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

  /** Front page groups and cards. A guide in docs/ not listed here stays in the sidebar. */
  coin: {
    groups: [
      { id: "use", title: "Use", icon: "i-lucide-wallet" },
      { id: "build", title: "Build", icon: "i-lucide-code" },
      { id: "run", title: "Run", icon: "i-lucide-server" },
    ],
    pages: [
      {
        slug: "user-guide",
        group: "use",
        title: "User guide",
        description: "Wallets, payments, escrow, subscriptions and key recovery.",
      },
      {
        slug: "tokenomics",
        group: "use",
        title: "Tokenomics",
        description: "ARCE supply, the fee market and what is still specified only.",
      },
      {
        slug: "developer-guide",
        group: "build",
        title: "Developer guide",
        description: "Architecture, the transaction lifecycle, the state tree and testing.",
      },
      {
        slug: "integration-guide",
        group: "build",
        title: "Integration guide",
        description: "Connect a backend, exchange or bot with webhooks, OIDC and compliance hooks.",
      },
      {
        slug: "api-reference",
        group: "build",
        title: "API reference",
        description: "REST endpoints, JSON-RPC methods and every error code.",
      },
      {
        slug: "embedding",
        group: "build",
        title: "Embedding",
        description: "Run a node or embed the ledger and SDKs in another service.",
      },
      {
        slug: "operator-guide",
        group: "run",
        title: "Operator guide",
        description: "Deploy, monitor, back up, restore and upgrade a node.",
      },
      {
        slug: "security",
        group: "run",
        title: "Security",
        description: "The threat model, compliance stance and how to report a problem.",
      },
    ],
  },
});
