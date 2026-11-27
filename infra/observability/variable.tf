# variable.tf

variable "project_id" {
  type        = string
  description = "Project id from google cloud"
}

variable "region" {
  type        = string
  description = "Project deployed"
}

variable "alert_emails" {
  type        = list(string)
  description = "Adresses qui reçoivent les alertes. Vide : les alertes existent et se voient dans la console, sans envoi."
  default     = []
}
