# main.tf

# Hébergement du front : l'application (Vite + React) est construite dans une image Docker (app/frontend/Dockerfile,
# nginx) et servie par Cloud Run. Cette stack crée tout ce qui l'entoure ; l'image elle-même est construite, envoyée
# et déployée par scripts/deploy_site.sh (la CI appelle le même script).

data "google_project" "this" {
  project_id = var.project_id
}

resource "google_project_service" "required" {
  for_each = toset([
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
  ])

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

# Registre où est envoyée l'image du front (une seule, mise à jour à chaque déploiement)
resource "google_artifact_registry_repository" "front" {
  repository_id = "front"
  location      = var.region
  format        = "DOCKER"
  description   = "Image du front"

  depends_on = [google_project_service.required]
}

# Identité d'exécution du front : aucun droit, il ne parle à aucune ressource GCP (le navigateur appelle la gateway)
resource "google_service_account" "front" {
  account_id   = "front-sa"
  display_name = "Identité du front (Cloud Run)"
}

resource "google_cloud_run_v2_service" "front" {
  name     = "front"
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  # lets the pipeline destroy the service
  deletion_protection = false

  template {
    service_account = google_service_account.front.email

    scaling {
      min_instance_count = var.min_instances
      max_instance_count = var.max_instances
    }

    containers {
      image = var.initial_image

      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }

        # CPU alloué seulement pendant une requête (facturation à la requête) : un site statique n'a rien à faire
        # entre deux visites, et le CPU « toujours alloué » exigerait au moins 512 Mi
        cpu_idle = true

        # démarrage à froid plus court (CPU majoré pendant le démarrage)
        startup_cpu_boost = true
      }
    }
  }

  # L'image est changée par scripts/deploy_site.sh (gcloud run services update) : Terraform ne la remet pas à zéro
  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
      client,
      client_version,
    ]
  }

  depends_on = [google_project_service.required]
}

# Un site public : tout le monde peut l'appeler. Seule l'API (gateway, jeton Discord) est protégée, pas les pages.
resource "google_cloud_run_v2_service_iam_member" "public" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.front.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# Le déploiement n'utilise plus Firebase Hosting : ces ressources sortent de l'état sans être détruites
# (le projet Firebase et le site existants restent en place, à supprimer à la main si besoin).
removed {
  from = google_firebase_hosting_site.this
  lifecycle {
    destroy = false
  }
}

removed {
  from = google_firebase_project.this
  lifecycle {
    destroy = false
  }
}

removed {
  from = google_project_service.apis
  lifecycle {
    destroy = false
  }
}
