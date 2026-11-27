# alerts.tf

# Alertes sur les problèmes critiques : retard de file, messages en échec, erreurs, latence, saturation.
# Chaque politique dit, dans sa documentation, quoi regarder et quoi faire.

locals {
  channels = [for c in google_monitoring_notification_channel.email : c.id]
}

# 1. La file des pixels prend du retard : les workers ne suivent pas
resource "google_monitoring_alert_policy" "pixel_queue_late" {
  display_name = "File des pixels en retard"
  combiner     = "OR"

  conditions {
    display_name = "Le plus vieux message attend depuis plus de 2 minutes"
    condition_threshold {
      filter          = "metric.type=\"pubsub.googleapis.com/subscription/oldest_unacked_message_age\" AND resource.type=\"pubsub_subscription\" AND resource.label.subscription_id=\"pixel-events-worker\""
      comparison      = "COMPARISON_GT"
      threshold_value = 120
      duration        = "300s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MAX"
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Les pixels s'accumulent dans `pixel-events` : `draw-pixel` ne suit pas. Regarder, sur le tableau de bord, les instances de `draw-pixel` (montée en charge) et les erreurs 5xx. Les messages ne sont pas perdus : ils attendent, et partent en DLQ après 10 échecs."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [google_project_service.monitoring]
}

# 2. Des messages sont tombés en file d'erreurs (DLQ) : un échec répété à traiter ou à rejouer
resource "google_monitoring_alert_policy" "dlq_not_empty" {
  display_name = "Messages en file d'erreurs (DLQ)"
  combiner     = "OR"

  conditions {
    display_name = "Une file d'erreurs contient des messages"
    condition_threshold {
      filter          = "metric.type=\"pubsub.googleapis.com/subscription/num_undelivered_messages\" AND resource.type=\"pubsub_subscription\" AND resource.label.subscription_id=monitoring.regex.full_match(\".*-dlq-hold\")"
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      duration        = "60s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MEAN"
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Un message a épuisé ses tentatives (10 pour les pixels, 5 pour les snapshots) et attend dans une souscription `*-dlq-hold` (gardé 7 jours). Pour le rejouer : `scripts/replay_dlq.py --queue pixel --dry-run`, puis sans `--dry-run`. Lire le message pour comprendre l'échec, puis le rejouer ou l'acquitter. Un événement invalide (hors grille, couleur inconnue) y arrive aussi."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [google_project_service.monitoring]
}

# 3. Taux d'erreurs 5xx élevé sur un service
resource "google_monitoring_alert_policy" "error_rate" {
  display_name = "Erreurs 5xx élevées"
  combiner     = "OR"

  conditions {
    display_name = "Plus de 3 erreurs 5xx par minute sur un service"
    condition_threshold {
      filter          = "metric.type=\"run.googleapis.com/request_count\" AND resource.type=\"cloud_run_revision\" AND metric.label.response_code_class=\"5xx\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0.05
      duration        = "300s"
      aggregations {
        alignment_period     = "60s"
        per_series_aligner   = "ALIGN_RATE"
        cross_series_reducer = "REDUCE_SUM"
        group_by_fields      = ["resource.label.service_name"]
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Un service répond en 5xx. Chercher dans Logs Explorer : `resource.labels.service_name=\"<service>\" severity>=ERROR`, puis suivre une requête avec son `correlation_id`."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [google_project_service.monitoring]
}

# 4. Latence trop haute sur les proxys (Discord abandonne au bout de 3 secondes)
resource "google_monitoring_alert_policy" "proxy_latency" {
  display_name = "Latence des proxys élevée"
  combiner     = "OR"

  conditions {
    display_name = "Latence p95 supérieure à 1,5 s"
    condition_threshold {
      filter          = "metric.type=\"run.googleapis.com/request_latencies\" AND resource.type=\"cloud_run_revision\" AND resource.label.service_name=one_of(\"proxy\",\"discord-proxy\")"
      comparison      = "COMPARISON_GT"
      threshold_value = 1500
      duration        = "300s"
      aggregations {
        alignment_period     = "60s"
        per_series_aligner   = "ALIGN_PERCENTILE_95"
        cross_series_reducer = "REDUCE_MAX"
        group_by_fields      = ["resource.label.service_name"]
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Le proxy met plus de 1,5 s à répondre pour 5 % des requêtes. Discord abandonne une commande après 3 s. Regarder le nombre d'instances et le CPU des conteneurs sur le tableau de bord."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [google_project_service.monitoring]
}

# 5. Saturation du cluster : CPU des nœuds
resource "google_monitoring_alert_policy" "node_cpu" {
  display_name = "CPU des nœuds du cluster saturé"
  combiner     = "OR"

  conditions {
    display_name = "CPU alloué utilisé au-dessus de 85 % pendant 10 minutes"
    condition_threshold {
      filter          = "metric.type=\"kubernetes.io/node/cpu/allocatable_utilization\" AND resource.type=\"k8s_node\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0.85
      duration        = "600s"
      aggregations {
        alignment_period     = "60s"
        per_series_aligner   = "ALIGN_MEAN"
        cross_series_reducer = "REDUCE_MAX"
        group_by_fields      = ["resource.label.cluster_name"]
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Les nœuds du cluster sont presque pleins. Le pool des exécuteurs monte jusqu'à son plafond (garde-fou de coût) : si les jobs de CI s'accumulent, ils attendent un nœud."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [google_project_service.monitoring]
}

# 6. Le snapshot échoue
resource "google_monitoring_alert_policy" "snapshot_failed" {
  display_name = "Échecs de snapshot"
  combiner     = "OR"

  conditions {
    display_name = "create-snapshot a échoué"
    condition_threshold {
      filter          = "metric.type=\"logging.googleapis.com/user/snapshot_failures\" AND resource.type=\"cloud_run_revision\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      duration        = "0s"
      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_SUM"
        cross_series_reducer = "REDUCE_SUM"
      }
      trigger {
        count = 1
      }
    }
  }

  documentation {
    mime_type = "text/markdown"
    content   = "Un passage de `create-snapshot` a échoué (Pub/Sub le réessaie, jusqu'à 5 fois). Le front et `/get` continuent de montrer le dernier snapshot réussi. Lire l'erreur dans les journaux du service ; un passage complet se relance en publiant `{\"full\": true}` sur le topic `snapshot`."
  }

  notification_channels = local.channels
  alert_strategy {
    auto_close = "1800s"
  }

  depends_on = [
    google_project_service.monitoring,
    google_logging_metric.snapshot_failures,
  ]
}
