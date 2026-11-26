# variable.tf

variable "project_id" {
  type        = string
  description = "Project id from google cloud"
}

variable "region" {
  type        = string
  description = "Project deployed"
}

variable "zone" {
  type        = string
  description = "Zone of the GKE cluster (created by the foundation stack)"
}

variable "cluster_name" {
  type        = string
  description = "Name of the GKE cluster (created by the foundation stack)"
}

variable "github_config_url" {
  type        = string
  description = "Repository the runners register to"
}

variable "github_app_id" {
  type        = string
  description = "ID of the GitHub App ARC authenticates with"
}

variable "github_app_installation_id" {
  type        = string
  description = "Installation ID of the GitHub App on the organization"
}

variable "github_app_private_key_secret" {
  type        = string
  description = "Secret Manager secret holding the GitHub App private key"
}

variable "runner_scale_set_name" {
  type        = string
  description = "Name of the runner scale set, used as the runs-on label in workflows"
}

variable "max_runners" {
  type        = number
  description = "Maximum number of runners at once (2 per runner node, 3 nodes max)"
  default     = 6
}

variable "arc_chart_version" {
  type        = string
  description = "Version of the Actions Runner Controller Helm charts"
  default     = "0.15.1"
}
