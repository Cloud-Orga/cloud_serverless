# dashboard.tf

# Tableau de bord unique : application serverless, files Pub/Sub, Firestore, cluster GKE et exécuteurs.
# Le contenu est dans dashboards/overview.json (lisible et relu en PR) ; il ne dépend d'aucun projet.
resource "google_monitoring_dashboard" "overview" {
  dashboard_json = file("${path.module}/dashboards/overview.json")

  # les métriques tirées des journaux doivent exister avant les graphiques qui les lisent
  depends_on = [
    google_logging_metric.pixels_rate_limited,
    google_logging_metric.application_errors,
  ]
}
