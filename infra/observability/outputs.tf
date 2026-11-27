# outputs.tf

output "dashboard_url" {
  description = "Tableau de bord dans la console Google Cloud"
  value       = "https://console.cloud.google.com/monitoring/dashboards/builder/${element(split("/", google_monitoring_dashboard.overview.id), 3)}?project=${var.project_id}"
}

output "alert_policies" {
  description = "Alertes créées"
  value = [
    google_monitoring_alert_policy.pixel_queue_late.display_name,
    google_monitoring_alert_policy.dlq_not_empty.display_name,
    google_monitoring_alert_policy.error_rate.display_name,
    google_monitoring_alert_policy.proxy_latency.display_name,
    google_monitoring_alert_policy.node_cpu.display_name,
    google_monitoring_alert_policy.snapshot_failed.display_name,
  ]
}
