# variables.tf

variable "project_id" {
  description = "ID du projet GCP"
  type        = string
}

variable "region" {
  description = "Région de déploiement"
  type        = string
}

variable "name" {
  description = "Nom de la fonction (aussi utilisé pour son compte de service : <name>-fn-sa)"
  type        = string
}

variable "source_dir" {
  description = "Dossier contenant le code de la fonction"
  type        = string
}

variable "entry_point" {
  description = "Nom de la fonction enregistrée dans le code (functions.HTTP)"
  type        = string
}

variable "source_bucket" {
  description = "Bucket qui reçoit le zip du code"
  type        = string
}

variable "runtime" {
  description = "Runtime Cloud Run functions"
  type        = string
  default     = "go127"
}

variable "memory" {
  description = "Mémoire allouée à la fonction"
  type        = string
  default     = "256M"
}

variable "timeout_seconds" {
  description = "Durée maximale d'une exécution"
  type        = number
  default     = 60
}

variable "min_instances" {
  description = "Instances gardées chaudes en permanence (0 = la fonction peut s'éteindre : démarrage à froid au prochain appel)"
  type        = number
  default     = 0
}

variable "max_instances" {
  description = "Plafond d'instances (null = valeur par défaut de la plateforme) ; 1 pour une fonction qui ne doit jamais tourner deux fois en même temps"
  type        = number
  default     = null
}

variable "concurrency" {
  description = "Requêtes traitées en même temps par une instance (null = valeur par défaut de la plateforme, soit 1 pour une fonction de 2e génération). Plus d'une demande au moins 1 vCPU (variable cpu)."
  type        = number
  default     = null
}

variable "cpu" {
  description = "CPU par instance, par exemple \"1\" (null = déduit de la mémoire : 0,17 vCPU pour 256M). Indispensable à 1 minimum pour que concurrency dépasse 1."
  type        = string
  default     = null
}

variable "env" {
  description = "Variables d'environnement de la fonction"
  type        = map(string)
  default     = {}
}

variable "wait_for" {
  description = "Ressources à créer avant la fonction (ex. droit de lire un secret) : évite l'échec du premier déploiement"
  type        = any
  default     = []
}

variable "secret_env" {
  description = "Secrets exposés en variables d'environnement : nom de la variable => secret_id dans Secret Manager"
  type        = map(string)
  default     = {}
}
