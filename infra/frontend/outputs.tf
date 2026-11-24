# outputs.tf

output "site_url" {
  description = "Adresse publique du front, à déclarer dans cors_allowed_origins (serverless) et dans les redirections OAuth2 de Discord. Forme prévisible : https://<service>-<numéro du projet>.<région>.run.app"
  value       = "https://${google_cloud_run_v2_service.front.name}-${data.google_project.this.number}.${var.region}.run.app"
}

output "service_urls" {
  description = "Adresses réelles du service, pour vérifier que site_url en fait partie"
  value       = google_cloud_run_v2_service.front.urls
}

output "service_name" {
  value = google_cloud_run_v2_service.front.name
}

output "image_repository" {
  description = "Où envoyer l'image : <image_repository>/front:<étiquette>"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.front.repository_id}"
}
