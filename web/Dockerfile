# syntax=docker/dockerfile:1

FROM node:22-alpine AS build
WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

# ---- runtime stage ----
FROM nginx:1.27-alpine AS runtime
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80

# Default nginx.conf; the gateway nginx (infra/nginx) terminates TLS and
# proxies to this container for static assets.
CMD ["nginx", "-g", "daemon off;"]