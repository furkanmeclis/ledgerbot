# syntax=docker/dockerfile:1.7
# Ledgerbot documentation site (Next.js standalone).
# The OpenAPI contracts live outside this folder and come in as a named context:
#   docker build --build-context contracts=../docs/contracts -t ledgerbot-docs .
# (compose.yml passes it automatically).

FROM node:22-alpine AS deps
WORKDIR /repo/docs-site
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

FROM node:22-alpine AS build
ENV NEXT_TELEMETRY_DISABLED=1
WORKDIR /repo/docs-site
COPY --from=contracts . /repo/docs/contracts
COPY --from=deps /repo/docs-site/node_modules ./node_modules
COPY . .
RUN npm run build \
  # No next/image in this site: drop the traced sharp/libvips binaries (~45 MB).
  && rm -rf .next/standalone/docs-site/node_modules/@img .next/standalone/docs-site/node_modules/sharp

FROM node:22-alpine
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
WORKDIR /app
COPY --from=build --chown=node:node /repo/docs-site/.next/standalone ./
COPY --from=build --chown=node:node /repo/docs-site/.next/static ./docs-site/.next/static
COPY --from=contracts --chown=node:node . ./docs/contracts
USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s CMD wget -qO- http://127.0.0.1:3000/api/health >/dev/null || exit 1
CMD ["node", "docs-site/server.js"]
