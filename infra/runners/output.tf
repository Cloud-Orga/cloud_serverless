# output.tf

output "runs_on_label" {
  value       = helm_release.runner_scale_set.name
  description = "Label to use in runs-on to target these runners"
}
