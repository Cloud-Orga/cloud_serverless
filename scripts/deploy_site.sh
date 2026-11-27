#!/usr/bin/env bash
#
# deploy_site.sh : construit l'image Docker du front pour un environnement, l'envoie et la déploie sur Cloud Run.
#
# CE QUE FAIT CE SCRIPT
#   1. Lit dans les sorties Terraform l'adresse de la gateway, sa clé API, l'adresse du front et le registre d'images
#      (stacks serverless et frontend : elles doivent déjà être appliquées sur l'environnement).
#   2. Construit l'image (app/frontend/Dockerfile) avec ces valeurs figées dedans : une image par environnement.
#   3. L'envoie dans Artifact Registry, puis met à jour le service Cloud Run « front » avec cette image.
#   4. Vérifie que le site répond.
#
# C'est aussi ce qu'exécute la CI (.github/workflows/deploy-site.yml) : une seule version de la logique, donc ce
# que vous lancez à la main est ce que fait une release.
#
# USAGE
#   DISCORD_CLIENT_ID=<identifiant> ./scripts/deploy_site.sh <env> [--build-only]
#   --build-only : construit l'image en local, n'envoie ni ne déploie rien (pour vérifier que ça compile).
#
# PRÉREQUIS
#   - DISCORD_CLIENT_ID : identifiant de l'application Discord (public). S'il manque, il est demandé sur un terminal.
#   - docker, terraform et gcloud ; gcloud connecté (gcloud auth login, et gcloud auth application-default login
#     pour Terraform). En CI, ce sont les identifiants de l'action d'authentification.
#
# RIEN DE SECRET : les variables VITE_* finissent dans le JavaScript servi aux navigateurs (clé de la gateway
# comprise : elle est visible dans l'onglet Réseau, la vraie protection est le jeton Discord vérifié côté serveur).
# Le jeton d'accès au registre passe par l'entrée standard de docker login, jamais en argument ni dans un fichier.
#
# TESTÉ : construction de l'image et lancement en local. L'envoi et le déploiement n'ont pas encore tourné par ce script.

set -euo pipefail

die() {
  printf 'Erreur : %s\n' "$*" >&2
  exit 1
}
info() { printf '%s\n' "$*"; }

[ $# -ge 1 ] || die "usage : $0 <env> [--build-only]"
ENV="$1"
BUILD_ONLY=0
[ "${2:-}" = "--build-only" ] && BUILD_ONLY=1
[[ "$ENV" =~ ^[a-z][a-z0-9]{1,9}$ ]] || die "env invalide : $ENV"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
for f in "infra/serverless/backends/$ENV.hcl" "infra/frontend/backends/$ENV.hcl" "infra/frontend/$ENV.tfvars"; do
  [ -f "$ROOT/$f" ] || die "$f absent : l'environnement $ENV n'existe pas (voir scripts/new_env.sh)"
done
for c in terraform docker; do command -v "$c" >/dev/null || die "$c est requis"; done
[ "$BUILD_ONLY" = 1 ] || command -v gcloud >/dev/null || die "gcloud est requis"

if [ -z "${DISCORD_CLIENT_ID:-}" ]; then
  [ -t 0 ] || die "DISCORD_CLIENT_ID absent (variable GitHub de l'environnement $ENV, ou à exporter)"
  read -rp "Identifiant de l'application Discord (public) : " DISCORD_CLIENT_ID
fi
[[ "$DISCORD_CLIENT_ID" =~ ^[0-9]{5,25}$ ]] || die "DISCORD_CLIENT_ID doit être un nombre (reçu : $DISCORD_CLIENT_ID)"

tfvar() { awk -F'"' -v k="$1" '$1 ~ "^"k"[[:space:]]*=" {print $2; exit}' "$ROOT/infra/frontend/$ENV.tfvars"; }
PROJECT_ID="$(tfvar project_id)"
REGION="$(tfvar region)"
[ -n "$PROJECT_ID" ] && [ -n "$REGION" ] || die "project_id ou region introuvable dans infra/frontend/$ENV.tfvars"

# --- 1. adresses lues dans Terraform --------------------------------------------------------------------
info "Lecture des sorties Terraform ($ENV)..."
terraform -chdir="$ROOT/infra/serverless" init -reconfigure -input=false -backend-config="backends/$ENV.hcl" >/dev/null
GATEWAY_URL="$(terraform -chdir="$ROOT/infra/serverless" output -raw gateway_url)" || die "sortie gateway_url introuvable : la stack serverless est-elle appliquée sur $ENV ?"
API_KEY="$(terraform -chdir="$ROOT/infra/serverless" output -raw api_key)" || die "sortie api_key introuvable"
[ -z "${GITHUB_ACTIONS:-}" ] || echo "::add-mask::$API_KEY" # jamais dans les journaux de la CI

terraform -chdir="$ROOT/infra/frontend" init -reconfigure -input=false -backend-config="backends/$ENV.hcl" >/dev/null
SITE_URL="$(terraform -chdir="$ROOT/infra/frontend" output -raw site_url)" || die "sortie site_url introuvable : la stack frontend est-elle appliquée sur $ENV ?"
SERVICE="$(terraform -chdir="$ROOT/infra/frontend" output -raw service_name)"
REPOSITORY="$(terraform -chdir="$ROOT/infra/frontend" output -raw image_repository)"
info "  site       : $SITE_URL"
info "  gateway    : $GATEWAY_URL"
info "  projet     : $PROJECT_ID"
info "  registre   : $REPOSITORY"

# L'adresse du site est écrite d'avance dans le CORS de la gateway : on vérifie qu'elle y figure bien
if [ "$BUILD_ONLY" = 0 ]; then
  URLS="$(terraform -chdir="$ROOT/infra/frontend" output -json service_urls)"
  grep -q "$SITE_URL" <<<"$URLS" || die "$SITE_URL n'est pas une adresse du service ($URLS) : corrigez cors_allowed_origins avant de déployer"
fi

# --- 2. image --------------------------------------------------------------------------------------------
TAG="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || date +%Y%m%d%H%M%S)"
IMAGE="$REPOSITORY/front:$TAG"
info "Construction de l'image $IMAGE ..."
docker build -f "$ROOT/app/frontend/Dockerfile" -t "$IMAGE" \
  --build-arg "VITE_DISCORD_CLIENT_ID=$DISCORD_CLIENT_ID" \
  --build-arg "VITE_DISCORD_REDIRECT_URI=$SITE_URL" \
  --build-arg "VITE_GATEWAY_URL=$GATEWAY_URL" \
  --build-arg "VITE_PIXEL_API_KEY=$API_KEY" \
  --build-arg "VITE_FIREBASE_PROJECT_ID=$PROJECT_ID" \
  "$ROOT"

if [ "$BUILD_ONLY" = 1 ]; then
  info "Construction réussie (--build-only : rien n'est envoyé)."
  exit 0
fi

# --- 3. envoi et déploiement ----------------------------------------------------------------------------
info "Envoi de l'image..."
gcloud auth print-access-token | docker login -u oauth2accesstoken --password-stdin "${REGION}-docker.pkg.dev" >/dev/null
docker push "$IMAGE"

info "Déploiement sur Cloud Run..."
gcloud run services update "$SERVICE" --image="$IMAGE" --region="$REGION" --project="$PROJECT_ID" --quiet

# --- 4. vérification ---------------------------------------------------------------------------------------
info "Vérification..."
code="$(curl -s -o /dev/null -m 30 -w '%{http_code}' --retry 5 --retry-delay 3 --retry-all-errors "$SITE_URL/")"
[ "$code" = "200" ] || die "le site répond $code sur $SITE_URL"
info ""
info "Site en ligne : $SITE_URL"
info "Rappel : l'adresse $SITE_URL doit figurer dans les redirections OAuth2 du portail Discord."
