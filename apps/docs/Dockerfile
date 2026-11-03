# The public documentation site at https://coin.anorebel.net.
#
# Build from the repository root, because the guides are docs/:
#
#   docker build -f apps/docs/Dockerfile -t coin-docs .
#
# The site reads docs/ directly; there is no copied content directory.

FROM node:26-slim AS build

COPY --from=oven/bun:1 /usr/local/bin/bun /usr/local/bin/bun

WORKDIR /src/apps/docs

# Install first, so the dependency layer survives guide edits.
COPY apps/docs/package.json apps/docs/bun.lock ./
RUN bun install --frozen-lockfile

COPY docs/ /src/docs/
COPY apps/docs/ ./

RUN bun run build

FROM node:26-slim

WORKDIR /app
RUN chown node:node /app
USER node

COPY --from=build --chown=node:node /src/apps/docs/.output ./.output

ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=3000

EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
