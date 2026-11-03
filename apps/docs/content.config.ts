import { fileURLToPath } from "node:url";

import { defineCollection, defineContentConfig, z } from "@nuxt/content";

/**
 * The guides are the repository's docs/ directory. There is no copy under
 * content/ on purpose: a second copy is a second thing to forget to update.
 * README is the repository's index of those files, not a guide; the site's
 * landing page takes its place.
 */
const docsDir = fileURLToPath(new URL("../../docs", import.meta.url));

export default defineContentConfig({
  collections: {
    docs: defineCollection({
      type: "page",
      source: {
        cwd: docsDir,
        include: "*.md",
        exclude: ["README.md"],
        prefix: "/",
      },
      schema: z.object({
        links: z
          .array(
            z.object({
              label: z.string(),
              icon: z.string(),
              to: z.string(),
              target: z.string().optional(),
            }),
          )
          .optional(),
      }),
    }),
  },
});
