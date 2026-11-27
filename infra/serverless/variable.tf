# variable.tf

variable "project_id" {
  type        = string
  description = "Project id from google cloud"
}

variable "region" {
  type        = string
  description = "Project deployed"
}

variable "cors_allowed_origins" {
  description = "Adresses des fronts autorisées à appeler /draw depuis un navigateur (liste vide = CORS fermé)"
  type        = list(string)
  default     = []
}

variable "discord_public_key" {
  description = "Clé publique (64 caractères hexadécimaux) de l'application Discord de cet environnement ; vide = à saisir à la main dans Secret Manager"
  type        = string
  default     = ""

  validation {
    condition     = var.discord_public_key == "" || can(regex("^[0-9a-f]{64}$", var.discord_public_key))
    error_message = "discord_public_key doit faire 64 caractères hexadécimaux en minuscules."
  }
}

variable "snapshot_schedule" {
  description = "Fréquence des snapshots, au format cron (UTC) : toutes les 5 minutes par défaut (sujet)"
  type        = string
  default     = "*/5 * * * *"
}

variable "rate_limit_per_minute" {
  description = "Pixels par minute et par utilisateur. Le sujet donne 20 en exemple, mais un trait de pinceau envoie un pixel par requête et dépasse vite ce quota (les pixels refusés sont abandonnés sans prévenir le front). 20000 laisse dessiner librement ; la limite se règle par utilisateur avec le champ limit de ratelimits/<utilisateur>."
  type        = number
  default     = 20000

  validation {
    condition     = var.rate_limit_per_minute >= 0 && floor(var.rate_limit_per_minute) == var.rate_limit_per_minute
    error_message = "rate_limit_per_minute doit être un entier positif ou nul."
  }
}
