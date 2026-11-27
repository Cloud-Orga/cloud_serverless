# apis.tf

# Cloud Monitoring et Cloud Logging sont actifs par défaut ; on les déclare pour que la stack ne dépende pas de ça.
resource "google_project_service" "monitoring" {
  for_each = toset([
    "monitoring.googleapis.com",
    "logging.googleapis.com",
  ])

  service            = each.value
  disable_on_destroy = false
}
