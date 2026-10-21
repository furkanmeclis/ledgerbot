# syntax=docker/dockerfile:1.7
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web ./web
RUN npm run build

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev --no-audit --no-fund

FROM node:22-alpine
ENV NODE_ENV=production PORT=3000
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY package.json ./
COPY server ./server
USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s CMD wget -qO- http://127.0.0.1:3000/api/companies >/dev/null || exit 1
CMD ["node", "server/index.js"]
