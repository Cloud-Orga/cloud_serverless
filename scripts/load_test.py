#!/usr/bin/env python3
"""
load_test.py : test de charge du canevas, avec le rapport des mesures de Cloud Monitoring.

Deux tests, parce que le chemin web exige un vrai jeton Discord (on ne peut pas le saturer avec de faux utilisateurs) :

  queue    publie des pixels directement dans le topic pixel-events, comme le fait le proxy, avec beaucoup
           d'utilisateurs fictifs. Mesure la file Pub/Sub, la montée en charge de draw-pixel et les écritures Firestore.
  http     envoie des requêtes à la gateway : lecture d'un pixel (GET /pixel), signature Discord invalide
           (POST /discord) ou dessin sans jeton (POST /draw). Mesure le débit et la latence des proxys.
  cleanup  supprime les pixels et les compteurs créés par le test (utilisateurs « load-* »).

Exemples (depuis la racine du dépôt, après « gcloud auth login ») :
  scripts/load_test.py queue --env dev --users 2000 --per-user 5
  scripts/load_test.py http  --env dev --target pixel --concurrency 40 --seconds 30
  scripts/load_test.py cleanup --env dev

SÉCURITÉ : refuse de viser prd sans --i-know-this-is-prd. Les pixels de test sont écrits dans le bloc 19_19 (coin bas
droit de la grille), à retirer avec « cleanup », puis supprimer tiles/19_19.png du bucket des snapshots si besoin.
Aucun secret n'est affiché : la clé de la gateway est lue dans les sorties Terraform et n'est jamais imprimée.
Bibliothèque standard uniquement.
"""
import argparse
import base64
import concurrent.futures as cf
import datetime as dt
import json
import random
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

PROJECTS = {"dev": "servtkt-cloudnative-dev", "prd": "servtkt-cloudnative-prd"}
USER_PREFIX = "load-"
BASE = 9500  # le test écrit dans x, y de 9500 à 9999 : bloc 19_19


def sh(*cmd):
    return subprocess.check_output(cmd, text=True).strip()


def token():
    return sh("gcloud", "auth", "print-access-token")


def api(url, project, tok, body=None, method=None):
    headers = {"Authorization": "Bearer " + tok, "x-goog-user-project": project, "Content-Type": "application/json"}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method or ("POST" if body is not None else "GET"))
    with urllib.request.urlopen(req, timeout=60) as r:
        raw = r.read()
        return json.loads(raw) if raw else {}


def now():
    return dt.datetime.now(dt.timezone.utc)


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


# ---- mesures Cloud Monitoring -----------------------------------------------------------------------------

def peak(project, tok, metric, extra, start, end, aligner="ALIGN_MAX", reducer="REDUCE_MAX", group=None, period=60):
    q = {
        "filter": f'metric.type="{metric}"' + (" AND " + extra if extra else ""),
        "interval.startTime": iso(start), "interval.endTime": iso(end),
        "aggregation.alignmentPeriod": f"{period}s", "aggregation.perSeriesAligner": aligner,
        "aggregation.crossSeriesReducer": reducer,
    }
    if group:
        q["aggregation.groupByFields"] = group
    url = f"https://monitoring.googleapis.com/v3/projects/{project}/timeSeries?" + urllib.parse.urlencode(q, doseq=True)
    try:
        res = api(url, project, tok).get("timeSeries", [])
    except urllib.error.HTTPError as e:
        return {"erreur": e.code}
    out = {}
    for s in res:
        key = ",".join((s["metric"]["labels"] if g.startswith("metric.") else s["resource"]["labels"]).get(g.split(".")[-1], "") for g in group) if group else "total"
        vals = [p["value"].get("doubleValue", p["value"].get("int64Value", 0)) for p in s["points"]]
        out[key] = max(float(v) for v in vals) if vals else 0
    return out


def report(project, tok, start, end):
    # Cloud Monitoring publie ses points avec plusieurs minutes de retard (file Pub/Sub, instances) : on attend
    print("\nattente de la publication des mesures (3 minutes)...")
    time.sleep(180)
    end = now()
    print("\n=== mesures Cloud Monitoring sur la période du test ===")
    sub = 'resource.type="pubsub_subscription" AND resource.label.subscription_id="pixel-events-worker"'
    age = peak(project, tok, "pubsub.googleapis.com/subscription/oldest_unacked_message_age", sub, start, end)
    backlog = peak(project, tok, "pubsub.googleapis.com/subscription/num_undelivered_messages", sub, start, end)
    print(f"file pixel-events : retard maximal du plus vieux message = {age.get('total', 'n/d')} s ; messages en attente (pic) = {backlog.get('total', 'n/d')}")
    inst = peak(project, tok, "run.googleapis.com/container/instance_count", 'resource.type="cloud_run_revision"', start, end,
                group=["resource.label.service_name"])
    print("instances Cloud Run (pic par service) :", {k: int(v) for k, v in sorted(inst.items())})
    err = peak(project, tok, "run.googleapis.com/request_count", 'resource.type="cloud_run_revision" AND metric.label.response_code_class="5xx"',
               start, end, aligner="ALIGN_DELTA", reducer="REDUCE_SUM", group=["resource.label.service_name"])
    print("erreurs 5xx par minute (pic par service) :", {k: int(v) for k, v in sorted(err.items())} or "aucune")
    dlq = peak(project, tok, "pubsub.googleapis.com/subscription/num_undelivered_messages",
               'resource.type="pubsub_subscription" AND resource.label.subscription_id="pixel-events-dlq-hold"', start, end)
    print(f"messages en file d'erreurs (pic) : {dlq.get('total', 'n/d')}")
    fs = peak(project, tok, "firestore.googleapis.com/api/request_count", 'resource.type="firestore.googleapis.com/Database"', start, end,
              aligner="ALIGN_RATE", reducer="REDUCE_SUM", group=["metric.label.api_method"])
    print("requêtes Firestore par méthode (pic, req/s) :", {k: round(v, 1) for k, v in sorted(fs.items())} or "aucune")
    codes = peak(project, tok, "firestore.googleapis.com/api/request_count", 'resource.type="firestore.googleapis.com/Database" AND metric.label.response_code!="OK"',
                 start, end, aligner="ALIGN_RATE", reducer="REDUCE_SUM", group=["metric.label.response_code"])
    print("requêtes Firestore en échec (pic, req/s) :", {k: round(v, 1) for k, v in sorted(codes.items())} or "aucune")


# ---- test de la file --------------------------------------------------------------------------------------

def count_pixels(project, tok):
    q = {"structuredAggregationQuery": {"structuredQuery": {"from": [{"collectionId": "pixels"}], "where": {"compositeFilter": {"op": "AND", "filters": [
        {"fieldFilter": {"field": {"fieldPath": "user_id"}, "op": "GREATER_THAN_OR_EQUAL", "value": {"stringValue": USER_PREFIX}}},
        {"fieldFilter": {"field": {"fieldPath": "user_id"}, "op": "LESS_THAN", "value": {"stringValue": USER_PREFIX + "~"}}}]}}},
        "aggregations": [{"alias": "n", "count": {}}]}}
    res = api(f"https://firestore.googleapis.com/v1/projects/{project}/databases/(default)/documents:runAggregationQuery", project, tok, q)
    return int(res[0]["result"]["aggregateFields"]["n"]["integerValue"])


def set_limits(project, tok, users, limit):
    """Fixe la limite de débit des utilisateurs de test, sans toucher aux autres (champ limit seulement)."""
    base = f"projects/{project}/databases/(default)/documents/ratelimits"
    for i in range(0, users, 400):
        writes = [{"update": {"name": f"{base}/{USER_PREFIX}{u}", "fields": {"limit": {"integerValue": str(limit)}}},
                   "updateMask": {"fieldPaths": ["limit"]}} for u in range(i, min(i + 400, users))]
        api(f"https://firestore.googleapis.com/v1/projects/{project}/databases/(default)/documents:batchWrite", project, tok, {"writes": writes})
    print(f"limite de débit fixée à {limit} pixels par minute pour {users} utilisateur(s) de test")


def cmd_queue(a):
    project = PROJECTS[a.env]
    tok = token()
    total = a.users * a.per_user
    if total > 250000:
        sys.exit("trop de pixels : le bloc 19_19 n'a que 250 000 cases")
    if a.per_user > 20000:
        print("ATTENTION : au-delà de 20 000 pixels par utilisateur, la limite de débit (20 000 par minute par défaut) en abandonnera une partie")
    msgs = []
    for i in range(total):
        user = i % a.users                      # intercalés : tous les utilisateurs écrivent en même temps
        msgs.append({"data": base64.b64encode(json.dumps({
            "x": BASE + (i % 500), "y": BASE + (i // 500), "color": 1 + (i % 16), "user_id": f"{USER_PREFIX}{user}"}).encode()).decode(),
            "attributes": {"correlation_id": f"load-{i}"}})
    print(f"{total} pixels, {a.users} utilisateurs, projet {project}")
    if a.limit_override is not None:
        set_limits(project, tok, a.users, a.limit_override)
    before = count_pixels(project, tok)
    start = now()
    t0 = time.time()
    lock = threading.Lock()
    sent = [0]

    def publish(batch):
        api(f"https://pubsub.googleapis.com/v1/projects/{project}/topics/pixel-events:publish", project, tok, {"messages": batch})
        with lock:
            sent[0] += len(batch)

    batches = [msgs[i:i + 500] for i in range(0, total, 500)]
    with cf.ThreadPoolExecutor(max_workers=a.publishers) as ex:
        futs = []
        for b in batches:
            futs.append(ex.submit(publish, b))
            if a.rate:
                time.sleep(len(b) / a.rate)       # débit demandé en messages par seconde
        for f in futs:
            f.result()
    t_pub = time.time() - t0
    print(f"publication terminée : {sent[0]} messages en {t_pub:.1f} s ({sent[0] / t_pub:.0f} msg/s)")

    # attente de l'écriture dans Firestore
    deadline = time.time() + a.timeout
    n = before
    last = 0
    while time.time() < deadline:
        n = count_pixels(project, tok)
        if time.time() - last > 10:
            print(f"  {int(time.time() - t0)} s : {n - before} / {total} pixels écrits")
            last = time.time()
        if n - before >= total:
            break
        time.sleep(3)
    t_all = time.time() - t0
    written = n - before
    print(f"\npixels écrits : {written} / {total} en {t_all:.1f} s depuis le début ({written / t_all:.0f} pixels/s de bout en bout)")
    if written < total:
        print(f"MANQUANTS : {total - written} (limite de débit, file d'erreurs ou délai de {a.timeout} s)")
    report(project, tok, start - dt.timedelta(minutes=1), now())


# ---- test HTTP --------------------------------------------------------------------------------------------

def percentile(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(len(xs) * p))] if xs else 0


def cmd_http(a):
    project = PROJECTS[a.env]
    gw = sh("terraform", f"-chdir=infra/serverless", "output", "-raw", "gateway_url")
    key = sh("terraform", f"-chdir=infra/serverless", "output", "-raw", "api_key")
    if a.target == "pixel":
        req_fn = lambda: urllib.request.Request(f"{gw}/pixel?x={random.randrange(10000)}&y={random.randrange(10000)}", headers={"x-api-key": key})
    elif a.target == "discord":
        req_fn = lambda: urllib.request.Request(f"{gw}/discord", data=b'{"type":2}', method="POST",
                                                headers={"Content-Type": "application/json", "X-Signature-Ed25519": "00" * 64, "X-Signature-Timestamp": "1"})
    else:
        req_fn = lambda: urllib.request.Request(f"{gw}/draw", data=b'{"x":1,"y":1,"color":1}', method="POST",
                                                headers={"Content-Type": "application/json", "x-api-key": key})
    lat, codes = [], {}
    lock = threading.Lock()
    stop = time.time() + a.seconds
    start = now()

    def worker():
        while time.time() < stop:
            t = time.time()
            try:
                with urllib.request.urlopen(req_fn(), timeout=20) as r:
                    c = r.status
            except urllib.error.HTTPError as e:
                c = e.code
            except Exception:
                c = "réseau"
            d = (time.time() - t) * 1000
            with lock:
                lat.append(d)
                codes[c] = codes.get(c, 0) + 1

    print(f"cible {a.target} sur {a.env} : {a.concurrency} connexions pendant {a.seconds} s")
    ths = [threading.Thread(target=worker) for _ in range(a.concurrency)]
    [t.start() for t in ths]
    [t.join() for t in ths]
    n = len(lat)
    print(f"\n{n} requêtes, {n / a.seconds:.0f} req/s | codes : {codes}")
    print(f"latence ms : p50={percentile(lat, .5):.0f}  p95={percentile(lat, .95):.0f}  p99={percentile(lat, .99):.0f}  max={max(lat):.0f}")
    tok = token()
    report(project, tok, start - dt.timedelta(minutes=1), now())


# ---- nettoyage --------------------------------------------------------------------------------------------

def drain_dlq(project, tok):
    """Acquitte les messages de test (user_id « load-* ») de la file d'erreurs ; laisse les autres intacts."""
    sub = f"https://pubsub.googleapis.com/v1/projects/{project}/subscriptions/pixel-events-dlq-hold"
    removed = 0
    for _ in range(200):  # garde-fou : jamais de boucle sans fin
        msgs = api(sub + ":pull", project, tok, {"maxMessages": 1000}).get("receivedMessages", [])
        if not msgs:
            break
        mine = []
        for m in msgs:
            try:
                user = json.loads(base64.b64decode(m["message"]["data"])).get("user_id", "")
            except ValueError:
                continue
            if str(user).startswith(USER_PREFIX):
                mine.append(m["ackId"])
        if mine:
            api(sub + ":acknowledge", project, tok, {"ackIds": mine})
            removed += len(mine)
    return removed


def cmd_cleanup(a):
    project = PROJECTS[a.env]
    tok = token()
    base = f"https://firestore.googleapis.com/v1/projects/{project}/databases/(default)/documents"
    q = {"structuredQuery": {"from": [{"collectionId": "pixels"}], "select": {"fields": [{"fieldPath": "user_id"}]},
                             "where": {"compositeFilter": {"op": "AND", "filters": [
                                 {"fieldFilter": {"field": {"fieldPath": "user_id"}, "op": "GREATER_THAN_OR_EQUAL", "value": {"stringValue": USER_PREFIX}}},
                                 {"fieldFilter": {"field": {"fieldPath": "user_id"}, "op": "LESS_THAN", "value": {"stringValue": USER_PREFIX + "~"}}}]}},
                             "limit": 20000}}
    deleted = 0
    while True:
        # une page à la fois : on supprime, puis on relit jusqu'à ce qu'il n'en reste plus
        res = api(f"{base}:runQuery", project, tok, q)
        got = [r["document"]["name"] for r in res if "document" in r]
        if not got:
            break
        for i in range(0, len(got), 400):
            api(f"{base}:batchWrite", project, tok, {"writes": [{"delete": n} for n in got[i:i + 400]]})
        deleted += len(got)
    names = []
    # compteurs de la limite de débit
    page = None
    while True:
        url = f"{base}/ratelimits?pageSize=300&mask.fieldPaths=count" + (f"&pageToken={page}" if page else "")
        res = api(url, project, tok)
        names += [d["name"] for d in res.get("documents", []) if d["name"].split("/")[-1].startswith(USER_PREFIX)]
        page = res.get("nextPageToken")
        if not page:
            break
    for i in range(0, len(names), 400):
        api(f"{base}:batchWrite", project, tok, {"writes": [{"delete": n} for n in names[i:i + 400]]})
    dlq = drain_dlq(project, tok)
    print(f"{dlq} message(s) de test retiré(s) de la file d'erreurs.")
    print(f"{deleted} pixels et {len(names)} compteurs supprimés sur {project}. Pensez à retirer tiles/19_19.png du bucket des snapshots (il contient les pixels de test).")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--i-know-this-is-prd", action="store_true", help="autorise la cible prd")
    sp = p.add_subparsers(dest="cmd", required=True)
    for name in ("queue", "http", "cleanup"):
        s = sp.add_parser(name)
        s.add_argument("--env", default="dev", choices=PROJECTS)
        if name == "queue":
            s.add_argument("--users", type=int, default=1000)
            s.add_argument("--per-user", type=int, default=5)
            s.add_argument("--rate", type=int, default=0, help="messages par seconde (0 = le plus vite possible)")
            s.add_argument("--limit-override", type=int, default=None,
                           help="limite de pixels par minute pour les utilisateurs de test (champ limit de ratelimits/<utilisateur>) : "
                                "ne touche ni à la configuration ni aux vrais utilisateurs ; supprimé par cleanup")
            s.add_argument("--publishers", type=int, default=8)
            s.add_argument("--timeout", type=int, default=600)
        if name == "http":
            s.add_argument("--target", choices=["pixel", "discord", "draw"], default="pixel")
            s.add_argument("--concurrency", type=int, default=40)
            s.add_argument("--seconds", type=int, default=30)
    a = p.parse_args()
    if a.env == "prd" and not a.i_know_this_is_prd:
        sys.exit("refus : prd est la référence de la soutenance (ajoutez --i-know-this-is-prd si c'est voulu)")
    {"queue": cmd_queue, "http": cmd_http, "cleanup": cmd_cleanup}[a.cmd](a)


if __name__ == "__main__":
    main()
