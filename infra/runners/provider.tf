# provider.tf

terraform {
  required_version = ">= 1.9"
  required_providers {
    google     = { source = "hashicorp/google", version = "~> 7.0" }
    kubernetes = { source = "hashicorp/kubernetes", version = "~> 2.36" }
    helm       = { source = "hashicorp/helm", version = "~> 3.0" }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# The cluster is created by the foundation stack: read its address and CA here
data "google_container_cluster" "main" {
  name     = var.cluster_name
  location = var.zone
}

# Short-lived OAuth token of whoever runs Terraform (the deployer in CI): no kubeconfig, no stored key
data "google_client_config" "current" {}

locals {
  cluster_host = "https://${data.google_container_cluster.main.endpoint}"
  cluster_ca   = base64decode(data.google_container_cluster.main.master_auth[0].cluster_ca_certificate)
}

provider "kubernetes" {
  host                   = local.cluster_host
  token                  = data.google_client_config.current.access_token
  cluster_ca_certificate = local.cluster_ca
}

provider "helm" {
  kubernetes = {
    host                   = local.cluster_host
    token                  = data.google_client_config.current.access_token
    cluster_ca_certificate = local.cluster_ca
  }
}
