# workers.tf

# Worker des pixels : lit pixel-events (souscription push) et écrit dans Firestore
module "draw_pixel" {
  source = "./modules/function"

  project_id    = var.project_id
  region        = var.region
  name          = "draw-pixel"
  entry_point   = "DrawPixel"
  source_dir    = "${path.module}/../../app/functions/draw_pixel"
  source_bucket = google_storage_bucket.functions_src.name

  env = {
    GCP_PROJECT  = var.project_id
    PALETTE_JSON = local.palette_json

    # Pixels par minute et par utilisateur (surchargeable par utilisateur : champ limit de ratelimits/{user})
    RATE_LIMIT_PER_MINUTE = tostring(var.rate_limit_per_minute)
  }

  # Mesuré sous 10 000 pixels d'un coup : avec une requête par instance, il fallait ~33 instances en même temps, et
  # Cloud Run abandonnait les requêtes qui attendaient trop longtemps une instance libre (jusqu'à 1 500 erreurs 5xx par
  # minute, rattrapées par les réessais de Pub/Sub). Le travail est surtout de l'attente réseau (Firestore) : une
  # instance peut traiter plusieurs messages à la fois. Le code est sûr en concurrence (client partagé, tests -race).
  concurrency = 40
  cpu         = "1"
  memory      = "512M"

  wait_for   = values(google_project_service.apis)
  depends_on = [google_firestore_database.main]
}

# Le worker écrit des documents Firestore : il ne peut ni créer ni supprimer de base
resource "google_project_iam_member" "draw_pixel_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = module.draw_pixel.service_account_member
}

# Identité avec laquelle Pub/Sub appelle le worker (jeton OIDC)
resource "google_service_account" "pubsub_push" {
  account_id   = "pubsub-push-sa"
  display_name = "Identité des souscriptions push Pub/Sub"
}

# Seule cette identité peut appeler le worker (il est privé)
resource "google_cloud_run_v2_service_iam_member" "push_invokes_draw_pixel" {
  project  = var.project_id
  location = var.region
  name     = module.draw_pixel.name
  role     = "roles/run.invoker"
  member   = google_service_account.pubsub_push.member
}

# L'agent de service Pub/Sub doit pouvoir fabriquer des jetons au nom de pubsub-push-sa
resource "google_service_account_iam_member" "pubsub_creates_push_tokens" {
  service_account_id = google_service_account.pubsub_push.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = google_project_service_identity.pubsub.member
}

# Worker des commandes admin
module "admin_cmd" {
  source = "./modules/function"

  project_id    = var.project_id
  region        = var.region
  name          = "admin-cmd"
  entry_point   = "AdminCmd"
  source_dir    = "${path.module}/../../app/functions/admin_cmd"
  source_bucket = google_storage_bucket.functions_src.name

  env = {
    GCP_PROJECT = var.project_id
  }

  wait_for   = values(google_project_service.apis)
  depends_on = [google_firestore_database.main]
}

resource "google_cloud_run_v2_service_iam_member" "push_invokes_admin_cmd" {
  project  = var.project_id
  location = var.region
  name     = module.admin_cmd.name
  role     = "roles/run.invoker"
  member   = google_service_account.pubsub_push.member
}
