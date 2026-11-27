# Runner scaling demo

Shows that our self-hosted GitHub runners scale horizontally through **node autoscaling**:
more jobs → more runner pods → more runner nodes, then back to zero when idle.

## How it works

| Piece | Where | Role |
| --- | --- | --- |
| Runner pool | `infra/foundation/gke.tf` | Autoscales from **0 to 3** `e2-standard-2` spot nodes; tainted so only runners land there |
| ARC scale set | `infra/runners/main.tf` | One ephemeral runner pod per job, **0 to 6** pods; each pod requests 700m CPU and 2 GiB, so **2 pods fit per node** |
| Cluster autoscaler | GKE (`OPTIMIZE_UTILIZATION`) | Adds a node when runner pods are pending, removes idle nodes quickly |

So 6 parallel jobs → 6 runner pods → 3 runner nodes. When the jobs end, the pods are deleted and the nodes are removed.

## Run it

1. Actions → **Load runners** → **Run workflow**, on `main`:
   - `env`: `prd` (the reference environment) or `dev`
   - `jobs`: `6` (more than 6 queue up: the scale set allows 6 runners at once)
   - `minutes`: `3` (how long each job holds its runner)
2. Or from a terminal:
   ```bash
   gh workflow run load-runners.yml -R Cloud-Orga/cloud_serverless --ref main -f env=prd -f jobs=6 -f minutes=3
   ```

Expected timeline: jobs queued ~1-2 min while nodes are added → 3 min of work → nodes removed ~1-2 min after the jobs end.

## What to show

- **GitHub**: in the run, each job's log starts with `job N on runner arc-runners-prd-runner-…`: our runners, not GitHub's.
- **Cluster** (after `gcloud container clusters get-credentials servtkt-prd-gke --zone europe-west1-b --project servtkt-cloudnative-prd`):
  ```bash
  kubectl get pods -n arc-runners -w                                   # runner pods appear, one per job
  kubectl get nodes -L cloud.google.com/gke-nodepool -w                # runner nodes: 0 → 3 → 0
  ```
- **Cloud Monitoring**: the node count of the `runners` node pool over time, which rises and falls with the load.
- **Autoscaler decisions** (Logs Explorer):
  ```
  resource.type="k8s_cluster"
  logName:"cluster-autoscaler-visibility"
  ```
  `scaleUp` events when the jobs start, `scaleDown` events when they end.
