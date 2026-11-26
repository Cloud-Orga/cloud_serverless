# snapshots.tf

# Chaîne des snapshots : Cloud Scheduler publie toutes les 5 minutes sur le topic « snapshot », une souscription push
# appelle create-snapshot, qui met à jour dans un bucket PRIVÉ une image par bloc de 500 x 500 pixels
# (uniquement les blocs modifiés). Le front et Discord y accèdent par des URL signées, jamais par un bucket public.

resource "google_project_service" "cloudscheduler" {
  service            = "cloudscheduler.googleapis.com"
  disable_on_destroy = false
}

# --- bucket privé -----------------------------------------------------------------------------------------

resource "google_storage_bucket" "snapshots" {
  name                        = "${var.project_id}-snapshots"
  location                    = var.region
  uniform_bucket_level_access = true

  # Jamais public, même par erreur de droit : l'accès se fait par URL signée
  public_access_prevention = "enforced"

  # Un environnement doit pouvoir être détruit même quand il contient des images (sujet : plus aucune ressource facturable)
  force_destroy = true

  # Historique : chaque réécriture d'un bloc garde l'ancienne version, supprimée après 7 jours
  versioning {
    enabled = true
  }

  lifecycle_rule {
    condition {
      days_since_noncurrent_time = 7
    }
    action {
      type = "Delete"
    }
  }
}

# --- topic, souscription et file d'erreurs ---------------------------------------------------------------

resource "google_pubsub_topic" "snapshot" {
  name                       = "snapshot"
  message_retention_duration = "604800s"

  depends_on = [google_project_service.pubsub]
}

resource "google_pubsub_topic" "snapshot_dlq" {
  name                       = "snapshot-dlq"
  message_retention_duration = "604800s"

  depends_on = [google_project_service.pubsub]
}

resource "google_pubsub_subscription" "snapshot_dlq_hold" {
  name  = "snapshot-dlq-hold"
  topic = google_pubsub_topic.snapshot_dlq.id

  message_retention_duration = "604800s"

  expiration_policy {
    ttl = ""
  }
}

resource "google_pubsub_subscription" "snapshot_worker" {
  name  = "snapshot-worker"
  topic = google_pubsub_topic.snapshot.id

  # un passage complet peut durer plusieurs minutes
  ack_deadline_seconds       = 300
  message_retention_duration = "604800s"

  retry_policy {
    minimum_backoff = "30s"
    maximum_backoff = "300s"
  }

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.snapshot_dlq.id
    max_delivery_attempts = 5
  }

  push_config {
    push_endpoint = module.create_snapshot.uri

    oidc_token {
      service_account_email = google_service_account.pubsub_push.email
    }
  }

  expiration_policy {
    ttl = ""
  }

  depends_on = [
    google_cloud_run_v2_service_iam_member.push_invokes_create_snapshot,
    google_service_account_iam_member.pubsub_creates_push_tokens,
  ]
}

resource "google_pubsub_topic_iam_member" "snapshot_dlq_publisher" {
  topic  = google_pubsub_topic.snapshot_dlq.id
  role   = "roles/pubsub.publisher"
  member = google_project_service_identity.pubsub.member
}

resource "google_pubsub_subscription_iam_member" "snapshot_worker_subscriber" {
  subscription = google_pubsub_subscription.snapshot_worker.id
  role         = "roles/pubsub.subscriber"
  member       = google_project_service_identity.pubsub.member
}

# --- planificateur ----------------------------------------------------------------------------------------

resource "google_cloud_scheduler_job" "snapshot" {
  name        = "snapshot-every-5-minutes"
  description = "Déclenche create-snapshot (images des blocs modifiés)"
  schedule    = var.snapshot_schedule
  time_zone   = "Etc/UTC"
  region      = var.region

  # message vide : passage normal. Pour un rattrapage complet, publier {"full": true} sur le topic à la main.
  pubsub_target {
    topic_name = google_pubsub_topic.snapshot.id
    data       = base64encode("{}")
  }

  depends_on = [google_project_service.cloudscheduler]
}

# --- fonction ---------------------------------------------------------------------------------------------

module "create_snapshot" {
  source = "./modules/function"

  project_id    = var.project_id
  region        = var.region
  name          = "create-snapshot"
  entry_point   = "CreateSnapshot"
  source_dir    = "${path.module}/../../app/functions/create_snapshot"
  source_bucket = google_storage_bucket.functions_src.name

  env = {
    GCP_PROJECT  = var.project_id
    BUCKET_NAME  = google_storage_bucket.snapshots.name
    PALETTE_JSON = local.palette_json
  }

  # une image de bloc fait 500 x 500 octets avant compression : 512 Mo suffisent largement
  memory          = "512M"
  timeout_seconds = 300

  # Jamais deux passages en même temps : ils pourraient écrire le même bloc avec des états différents
  max_instances = 1

  wait_for = values(google_project_service.apis)

  depends_on = [google_firestore_database.main]
}

# Lecture seule de Firestore : cette fonction ne peut rien écrire dans la base
resource "google_project_iam_member" "create_snapshot_datastore" {
  project = var.project_id
  role    = "roles/datastore.viewer"
  member  = module.create_snapshot.service_account_member
}

# Écriture dans le bucket des snapshots, et nulle part ailleurs
resource "google_storage_bucket_iam_member" "create_snapshot_bucket" {
  bucket = google_storage_bucket.snapshots.name
  role   = "roles/storage.objectAdmin"
  member = module.create_snapshot.service_account_member
}

# Seule l'identité des souscriptions push peut appeler la fonction (elle est privée)
resource "google_cloud_run_v2_service_iam_member" "push_invokes_create_snapshot" {
  project  = var.project_id
  location = var.region
  name     = module.create_snapshot.name
  role     = "roles/run.invoker"
  member   = google_service_account.pubsub_push.member
}
