<script setup lang="ts">
/*
 * Replaces the layer's app.vue for the sidebar: docs/ is flat, so the layer
 * would list it alphabetically; this orders it from app.config's catalogue.
 */
import type { ContentNavigationItem } from "@nuxt/content";

const appConfig = useAppConfig();
const site = useSiteConfig();
const { coin } = appConfig as unknown as {
  coin: {
    groups: { id: string; title: string; icon: string }[];
    pages: { slug: string; group: string; title: string }[];
  };
};

useDocusShortcuts();

useHead({
  meta: [{ name: "viewport", content: "width=device-width, initial-scale=1" }],
  htmlAttrs: { lang: "en" },
});

useSeoMeta({
  titleTemplate: appConfig.seo.titleTemplate,
  title: appConfig.seo.title,
  description: appConfig.seo.description,
  ogSiteName: site.name,
});

function grouped(pages: ContentNavigationItem[]): ContentNavigationItem[] {
  const byPath = new Map(pages.map((page) => [page.path, page]));
  const placed = new Set<string>();
  const groups = coin.groups.map((group) => ({
    title: group.title,
    icon: group.icon,
    path: `/${group.id}`,
    children: coin.pages
      .filter((page) => page.group === group.id)
      .flatMap((entry) => {
        const page = byPath.get(`/${entry.slug}`);
        if (!page) return [];
        placed.add(page.path);
        return [{ ...page, title: entry.title }];
      }),
  }));
  const rest = pages.filter((page) => !placed.has(page.path));
  if (rest.length) {
    groups.push({ title: "More", icon: "i-lucide-files", path: "/more", children: rest });
  }
  return groups.filter((group) => group.children.length);
}

const { data: navigation } = await useAsyncData(
  "navigation_docs",
  () => queryCollectionNavigation("docs"),
  { transform: grouped },
);

provide("navigation", navigation);
</script>

<template>
  <UApp>
    <NuxtLoadingIndicator color="var(--ui-primary)" />
    <AppHeader />
    <NuxtLayout>
      <NuxtPage />
    </NuxtLayout>
    <AppFooter />
    <ClientOnly>
      <AppSearch :navigation="navigation" />
    </ClientOnly>
  </UApp>
</template>
