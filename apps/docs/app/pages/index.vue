<script setup lang="ts">
// The front page: every guide in docs/, grouped as app.config says.
const appConfig = useAppConfig();
const { coin } = appConfig as unknown as {
  coin: {
    groups: { id: string; title: string; icon: string }[];
    pages: { slug: string; group: string; title: string; description: string }[];
  };
};

useSeoMeta({ title: "AnoRebelCoin documentation", description: appConfig.seo.description });

const groups = coin.groups
  .map((group) => ({ ...group, pages: coin.pages.filter((page) => page.group === group.id) }))
  .filter((group) => group.pages.length);
</script>

<template>
  <UMain>
    <UPageHero
      title="AnoRebelCoin"
      description="The native asset of AnoRebelNetwork: a currency-agnostic settlement network for small teams and interconnected organizations, with encrypted memos and opt-in compliance."
      :links="[
        { label: 'User guide', to: '/user-guide', icon: 'i-lucide-arrow-right', trailing: true },
        { label: 'Developer guide', to: '/developer-guide', color: 'neutral', variant: 'subtle' },
      ]"
    >
      <template #headline>
        <UBadge variant="subtle" color="neutral" class="font-mono">ARCE · ARNE</UBadge>
      </template>
    </UPageHero>

    <UContainer class="pb-24 space-y-14">
      <section v-for="group in groups" :key="group.id" :aria-labelledby="`group-${group.id}`">
        <div class="mb-5 flex items-center gap-3">
          <UIcon :name="group.icon" class="size-5 text-primary shrink-0" />
          <h2 :id="`group-${group.id}`" class="text-xl font-semibold text-highlighted">
            {{ group.title }}
          </h2>
        </div>
        <UPageGrid>
          <UPageCard
            v-for="page in group.pages"
            :key="page.slug"
            :title="page.title"
            :description="page.description"
            :to="`/${page.slug}`"
            variant="subtle"
          />
        </UPageGrid>
      </section>

      <p class="max-w-2xl text-sm text-muted">
        Nothing here is audited or suitable for real money yet. Every guide says
        what ships, what is specified but not built, and what is not planned.
      </p>
    </UContainer>
  </UMain>
</template>
