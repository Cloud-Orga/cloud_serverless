# main.tf

locals {
  arc_charts = "oci://ghcr.io/actions/actions-runner-controller-charts"
}

# 1. The controller: manages the runner scale sets. No toleration, so it lands on the system pool.
resource "helm_release" "arc_controller" {
  name             = "arc"
  namespace        = "arc-systems"
  create_namespace = true
  repository       = local.arc_charts
  chart            = "gha-runner-scale-set-controller"
  version          = var.arc_chart_version
}

# 2. The GitHub App credentials, read from Secret Manager and handed to ARC as a Kubernetes secret
data "google_secret_manager_secret_version" "github_app_key" {
  secret = var.github_app_private_key_secret
}

resource "kubernetes_namespace" "runners" {
  metadata {
    name = "arc-runners"
  }
}

resource "kubernetes_secret" "github_app" {
  metadata {
    name      = "github-app"
    namespace = kubernetes_namespace.runners.metadata[0].name
  }

  # key names expected by the gha-runner-scale-set chart
  data = {
    github_app_id              = var.github_app_id
    github_app_installation_id = var.github_app_installation_id
    github_app_private_key     = data.google_secret_manager_secret_version.github_app_key.secret_data
  }
}

# 3. The runner scale set: its name is the runs-on label. One ephemeral runner pod per job, 0 when idle.
resource "helm_release" "runner_scale_set" {
  name       = var.runner_scale_set_name
  namespace  = kubernetes_namespace.runners.metadata[0].name
  repository = local.arc_charts
  chart      = "gha-runner-scale-set"
  version    = var.arc_chart_version

  values = [yamlencode({
    githubConfigUrl    = var.github_config_url
    githubConfigSecret = kubernetes_secret.github_app.metadata[0].name
    minRunners         = 0
    maxRunners         = var.max_runners

    template = {
      spec = {
        # only on the runner pool: its label attracts, its taint keeps everything else away
        nodeSelector = { workload = "runners" }
        tolerations = [{
          key      = "workload"
          operator = "Equal"
          value    = "runners"
          effect   = "NoSchedule"
        }]
        containers = [{
          name    = "runner"
          image   = "ghcr.io/actions/actions-runner:latest"
          command = ["/home/runner/run.sh"]
          # sized so that 2 runners fit on one e2-standard-2 node
          resources = {
            requests = { cpu = "700m", memory = "2Gi" }
            limits   = { memory = "3Gi" }
          }
        }]
      }
    }
  })]

  # the scale set needs the controller's custom resources; on destroy, removed before the controller
  depends_on = [helm_release.arc_controller]
}
