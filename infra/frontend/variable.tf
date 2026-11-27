# variable.tf

variable "project_id" {
  type        = string
  description = "Project id from google cloud"
}

variable "region" {
  type        = string
  description = "Project deployed"
}

variable "min_instances" {
  type        = number
  description = "Instances toujours chaudes du front (0 = rien de facturé quand personne ne visite, démarrage à froid d'une ou deux secondes)"
  default     = 0
}

variable "max_instances" {
  type        = number
  description = "Plafond d'instances : borne la facture si le trafic explose"
  default     = 10
}

variable "initial_image" {
  type        = string
  description = "Image de départ, le temps que la première vraie image soit construite : scripts/deploy_site.sh la remplace"
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}
