# Dockerfile : le front (Vite + React) construit puis servi par nginx, pour Cloud Run.
#
# Étape 1 construit les fichiers statiques ; étape 2 ne garde que ces fichiers et nginx (pas de Node en production).
# Les adresses sont figées à la construction (une image par environnement) : les variables VITE_* finissent dans le
# JavaScript servi aux navigateurs, rien de secret ne doit y passer.

FROM node:22-alpine AS build
WORKDIR /repo

# La palette est partagée avec le back : config/palette.json est lue par le front (voir vite.config.ts).
COPY config ./config
WORKDIR /repo/app/frontend
COPY app/frontend/package.json app/frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY app/frontend ./

ARG VITE_DISCORD_CLIENT_ID
ARG VITE_DISCORD_REDIRECT_URI
ARG VITE_GATEWAY_URL
ARG VITE_PIXEL_API_KEY
ARG VITE_FIREBASE_PROJECT_ID
RUN npm run build

# nginx « unprivileged » : tourne sans être root et écoute sur 8080, le port que Cloud Run attend par défaut
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY app/frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/app/frontend/dist /usr/share/nginx/html
EXPOSE 8080
