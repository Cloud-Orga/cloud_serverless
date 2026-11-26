# outputs.tf

output "gateway_url" {
  description = "URL publique de l'API"
  value       = "https://${google_api_gateway_gateway.pixel.default_hostname}"
}

output "api_key" {
  description = "Clé API à passer dans le header x-api-key"
  value       = google_apikeys_key.pixel.key_string
  sensitive   = true
}

output "snapshots_bucket" {
  description = "Bucket privé des snapshots (un PNG par bloc de 500 x 500 sous tiles/, métadonnées sous meta/)"
  value       = google_storage_bucket.snapshots.name
}
