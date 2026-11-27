# metrics.tf

# Métriques tirées des journaux : ce que Cloud Monitoring ne mesure pas seul (le métier de l'application).
# Les journaux de toutes les fonctions sont en JSON structuré (champ message, severity, correlation_id).

locals {
  # services Cloud Run du projet (une fonction ou un site = un service)
  services = ["proxy", "discord-proxy", "draw-pixel", "get-pixel", "create-snapshot", "render-grid", "admin-cmd"]
}

# Pixels abandonnés parce que l'utilisateur a dépassé sa limite par minute (draw-pixel)
resource "google_logging_metric" "pixels_rate_limited" {
  name        = "pixels_rate_limited"
  description = "Pixels abandonnés par la limite de débit"
  filter      = "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"draw-pixel\" AND jsonPayload.message=\"pixel abandonné : limite de débit\""

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
    unit        = "1"
  }

  depends_on = [google_project_service.monitoring]
}

# Erreurs applicatives, par service : tout ce qu'une de nos fonctions journalise en ERROR
resource "google_logging_metric" "application_errors" {
  name        = "application_errors"
  description = "Journaux en erreur des fonctions du projet, par service"
  filter      = "resource.type=\"cloud_run_revision\" AND severity>=ERROR AND resource.labels.service_name=(${join(" OR ", [for s in local.services : "\"${s}\""])})"

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
    unit        = "1"

    labels {
      key         = "service"
      value_type  = "STRING"
      description = "Service Cloud Run"
    }
  }

  label_extractors = {
    "service" = "EXTRACT(resource.labels.service_name)"
  }

  depends_on = [google_project_service.monitoring]
}

# Un passage de create-snapshot qui échoue (Pub/Sub le réessaiera, mais une série d'échecs est à voir)
resource "google_logging_metric" "snapshot_failures" {
  name        = "snapshot_failures"
  description = "Échecs de create-snapshot"
  filter      = "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"create-snapshot\" AND jsonPayload.message=\"snapshot échoué\""

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
    unit        = "1"
  }

  depends_on = [google_project_service.monitoring]
}
