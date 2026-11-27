#!/usr/bin/env python3
"""
replay_dlq.py : remet dans la file d'origine les messages d'une file d'erreurs (DLQ).

Un message qui échoue 10 fois (pixels) ou 5 fois (snapshots) est envoyé dans une file d'erreurs, où il est gardé
7 jours (souscriptions « *-dlq-hold »). Cet outil le republie sur son topic d'origine, puis l'acquitte dans la file
d'erreurs, seulement APRÈS une publication réussie : un échec au milieu ne perd rien (au pire un message est rejoué
deux fois, ce que les workers supportent : ils sont idempotents).

Usage (depuis la racine du dépôt, après « gcloud auth login ») :
  scripts/replay_dlq.py --queue pixel --dry-run        regarde sans rien modifier
  scripts/replay_dlq.py --queue pixel                  rejoue (demande confirmation)
  scripts/replay_dlq.py --queue pixel --max 500 --yes  rejoue au plus 500 messages, sans confirmation

Files gérées : pixel (pixel-events), snapshot (snapshot).
REFUS VOLONTAIRE pour discord : une commande Discord porte un jeton d'interaction valable 15 minutes (la réponse
ne partirait plus) et un message admin rejoué referait une action. Ces messages restent à lire à la main.

L'heure d'origine du message est recopiée dans l'attribut original_publish_time : le worker des pixels s'en sert pour
ordonner le message. Sans cela, un rejeu (publié maintenant) écraserait un pixel qu'un autre utilisateur a redessiné
entre-temps.

SÉCURITÉ : refuse prd sans --i-know-this-is-prd. Le contenu des messages n'est pas affiché, sauf pour les pixels
(coordonnées et identifiant d'utilisateur, déjà publics dans Firestore). Bibliothèque standard uniquement.
"""
import argparse
import base64
import json
import subprocess
import sys
import urllib.request

PROJECTS = {"dev": "servtkt-cloudnative-dev", "prd": "servtkt-cloudnative-prd"}

# file d'erreurs -> topic d'origine
QUEUES = {
    "pixel": {"subscription": "pixel-events-dlq-hold", "topic": "pixel-events"},
    "snapshot": {"subscription": "snapshot-dlq-hold", "topic": "snapshot"},
}

# attributs ajoutés par Pub/Sub aux messages de la file d'erreurs : à ne pas recopier
DEAD_LETTER_PREFIX = "CloudPubSubDeadLetter"
ORIGINAL_TIME_SOURCE = "CloudPubSubDeadLetterSourceTopicPublishTime"


def token():
    return subprocess.check_output(["gcloud", "auth", "print-access-token"], text=True).strip()


def make_api(project, tok):
    def api(url, body=None):
        req = urllib.request.Request(
            url, data=json.dumps(body).encode() if body is not None else None, method="POST" if body is not None else "GET",
            headers={"Authorization": "Bearer " + tok, "x-goog-user-project": project, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return json.loads(raw) if raw else {}
    return api


def replay_attributes(attrs):
    """Attributs du message rejoué : ceux d'origine, sans ceux de la file d'erreurs, avec l'heure d'origine."""
    out = {k: v for k, v in (attrs or {}).items() if not k.startswith(DEAD_LETTER_PREFIX)}
    # une heure déjà recopiée par un rejeu précédent reste la vraie heure d'origine
    if "original_publish_time" not in out and attrs and attrs.get(ORIGINAL_TIME_SOURCE):
        out["original_publish_time"] = attrs[ORIGINAL_TIME_SOURCE]
    out["replayed"] = "true"
    return out


def describe(queue, messages):
    """Résumé lisible, sans exposer de contenu sensible."""
    lines = [f"{len(messages)} message(s) en file d'erreurs"]
    if queue == "pixel" and messages:
        users = {}
        for m in messages:
            try:
                d = json.loads(base64.b64decode(m["message"]["data"]))
                users[d.get("user_id", "?")] = users.get(d.get("user_id", "?"), 0) + 1
            except ValueError:
                users["(illisible)"] = users.get("(illisible)", 0) + 1
        top = sorted(users.items(), key=lambda kv: -kv[1])[:5]
        lines.append("  par utilisateur (les 5 premiers) : " + ", ".join(f"{u} : {n}" for u, n in top))
    first = messages[0]["message"].get("attributes", {}) if messages else {}
    if first.get(ORIGINAL_TIME_SOURCE):
        lines.append(f"  plus ancien vu : {first[ORIGINAL_TIME_SOURCE]}")
    return "\n".join(lines)


def run(api, project, queue, max_messages, dry_run, confirm, out=print):
    """Rejoue la file. `api` et `confirm` sont injectables pour les tests. Renvoie (rejoués, vus)."""
    q = QUEUES[queue]
    sub = f"https://pubsub.googleapis.com/v1/projects/{project}/subscriptions/{q['subscription']}"
    pub = f"https://pubsub.googleapis.com/v1/projects/{project}/topics/{q['topic']}:publish"

    # 1. lecture d'un premier lot, pour montrer ce qui va être rejoué
    first = api(sub + ":pull", {"maxMessages": min(1000, max_messages)}).get("receivedMessages", [])
    if not first:
        out("la file d'erreurs est vide : rien à rejouer.")
        return 0, 0
    out(describe(queue, first))
    if dry_run:
        # rendre les messages immédiatement visibles à nouveau
        api(sub + ":modifyAckDeadline", {"ackIds": [m["ackId"] for m in first], "ackDeadlineSeconds": 0})
        out("--dry-run : rien n'a été rejoué ni acquitté.")
        return 0, len(first)
    if not confirm(f"Rejouer sur {q['topic']} ({project}) ? [oui/N] "):
        api(sub + ":modifyAckDeadline", {"ackIds": [m["ackId"] for m in first], "ackDeadlineSeconds": 0})
        out("annulé.")
        return 0, len(first)

    # 2. republication par lots, acquittement après chaque lot publié
    replayed = seen = 0
    batch = first
    while batch and seen < max_messages:
        batch = batch[: max_messages - seen]
        seen += len(batch)
        messages = [{"data": m["message"]["data"], "attributes": replay_attributes(m["message"].get("attributes"))} for m in batch]
        for i in range(0, len(messages), 500):
            chunk = messages[i:i + 500]
            api(pub, {"messages": chunk})            # une erreur ici arrête tout : rien de ce lot n'est acquitté
            api(sub + ":acknowledge", {"ackIds": [m["ackId"] for m in batch[i:i + 500]]})
            replayed += len(chunk)
        out(f"  {replayed} message(s) rejoué(s)")
        if seen >= max_messages:
            break
        batch = api(sub + ":pull", {"maxMessages": min(1000, max_messages - seen)}).get("receivedMessages", [])
    out(f"terminé : {replayed} message(s) remis sur {q['topic']}.")
    return replayed, seen


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--queue", required=True, choices=["pixel", "snapshot", "discord"])
    p.add_argument("--env", default="dev", choices=PROJECTS)
    p.add_argument("--max", type=int, default=10000, help="nombre maximal de messages rejoués")
    p.add_argument("--dry-run", action="store_true", help="affiche ce qui serait rejoué, sans rien modifier")
    p.add_argument("--yes", action="store_true", help="sans demande de confirmation")
    p.add_argument("--i-know-this-is-prd", action="store_true", help="autorise la cible prd")
    a = p.parse_args()
    if a.queue == "discord":
        sys.exit("refus : une commande Discord porte un jeton d'interaction valable 15 minutes (la réponse ne partirait plus), "
                 "et rejouer un message admin referait une action. Voir l'en-tête du script.")
    if a.env == "prd" and not a.i_know_this_is_prd:
        sys.exit("refus : prd est la référence de la soutenance (ajoutez --i-know-this-is-prd si c'est voulu)")
    project = PROJECTS[a.env]

    def confirm(prompt):
        if a.yes:
            return True
        if not sys.stdin.isatty():
            sys.exit("pas de terminal pour confirmer (utilisez --yes en connaissance de cause)")
        return input(prompt).strip().lower() == "oui"

    run(make_api(project, token()), project, a.queue, a.max, a.dry_run, confirm)


if __name__ == "__main__":
    main()
