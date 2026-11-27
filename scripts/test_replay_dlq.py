"""Tests de replay_dlq.py avec un faux Pub/Sub : python3 -m unittest scripts/test_replay_dlq.py"""
import base64
import importlib.util
import json
import os
import unittest

spec = importlib.util.spec_from_file_location("replay", os.path.join(os.path.dirname(__file__), "replay_dlq.py"))
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)

T0 = "2026-11-27T13:40:00.123Z"


def dlq_message(i, user="u1"):
    data = base64.b64encode(json.dumps({"x": i, "y": 1, "color": 1, "user_id": user}).encode()).decode()
    return {"ackId": f"ack-{i}", "message": {"data": data, "attributes": {
        "correlation_id": f"c{i}", "type": "pixel.draw",
        "CloudPubSubDeadLetterSourceDeliveryCount": "5",
        "CloudPubSubDeadLetterSourceSubscription": "pixel-events-worker",
        "CloudPubSubDeadLetterSourceTopicPublishTime": T0}}}


class FauxPubSub:
    """Garde l'ordre des appels pour vérifier qu'on publie avant d'acquitter."""
    def __init__(self, messages, fail_publish=False):
        self.queue = list(messages)
        self.calls, self.published, self.acked = [], [], []
        self.fail_publish = fail_publish

    def __call__(self, url, body=None):
        op = url.rsplit(":", 1)[-1]
        self.calls.append(op)
        if op == "pull":
            n = body["maxMessages"]
            got, self.queue = self.queue[:n], self.queue[n:]
            return {"receivedMessages": got}
        if op == "publish":
            if self.fail_publish:
                raise RuntimeError("Pub/Sub indisponible")
            self.published += body["messages"]
            return {}
        if op == "acknowledge":
            self.acked += body["ackIds"]
            return {}
        return {}


class TestAttributs(unittest.TestCase):
    def test_retire_ceux_de_la_file_d_erreurs_et_garde_l_heure_d_origine(self):
        out = replay.replay_attributes(dlq_message(1)["message"]["attributes"])
        self.assertEqual(out["correlation_id"], "c1")
        self.assertEqual(out["original_publish_time"], T0)
        self.assertEqual(out["replayed"], "true")
        self.assertFalse([k for k in out if k.startswith("CloudPubSubDeadLetter")])

    def test_un_deuxieme_rejeu_garde_la_premiere_heure(self):
        out = replay.replay_attributes({"original_publish_time": "2026-01-01T00:00:00Z", replay.ORIGINAL_TIME_SOURCE: T0})
        self.assertEqual(out["original_publish_time"], "2026-01-01T00:00:00Z")

    def test_message_sans_attributs(self):
        self.assertEqual(replay.replay_attributes(None), {"replayed": "true"})


class TestRejeu(unittest.TestCase):
    def go(self, api, **kw):
        out = []
        r = replay.run(api, "p", kw.pop("queue", "pixel"), kw.pop("max_messages", 10000), kw.pop("dry_run", False),
                       kw.pop("confirm", lambda _: True), out=out.append)
        return r, "\n".join(out)

    def test_publie_puis_acquitte_et_recopie_les_donnees(self):
        api = FauxPubSub([dlq_message(i) for i in range(3)])
        (replayed, seen), _ = self.go(api)
        self.assertEqual((replayed, seen), (3, 3))
        self.assertEqual(len(api.published), 3)
        self.assertEqual(sorted(api.acked), ["ack-0", "ack-1", "ack-2"])
        # les données sont identiques, et l'heure d'origine est jointe
        self.assertEqual(api.published[0]["data"], dlq_message(0)["message"]["data"])
        self.assertEqual(api.published[0]["attributes"]["original_publish_time"], T0)
        # on ne retire rien de la file d'erreurs avant d'avoir publié
        self.assertLess(api.calls.index("publish"), api.calls.index("acknowledge"))

    def test_un_echec_de_publication_n_acquitte_rien(self):
        api = FauxPubSub([dlq_message(i) for i in range(3)], fail_publish=True)
        with self.assertRaises(RuntimeError):
            self.go(api)
        self.assertEqual(api.acked, [], "rien ne doit être acquitté si la publication échoue")

    def test_dry_run_ne_modifie_rien(self):
        api = FauxPubSub([dlq_message(i) for i in range(3)])
        (replayed, seen), out = self.go(api, dry_run=True)
        self.assertEqual((replayed, seen), (0, 3))
        self.assertEqual(api.published, [])
        self.assertEqual(api.acked, [])
        self.assertIn("--dry-run", out)
        self.assertIn("modifyAckDeadline", api.calls)  # les messages redeviennent visibles tout de suite

    def test_refus_de_confirmer_ne_modifie_rien(self):
        api = FauxPubSub([dlq_message(i) for i in range(3)])
        (replayed, _), out = self.go(api, confirm=lambda _: False)
        self.assertEqual(replayed, 0)
        self.assertEqual((api.published, api.acked), ([], []))
        self.assertIn("annulé", out)

    def test_file_vide(self):
        (replayed, seen), out = self.go(FauxPubSub([]))
        self.assertEqual((replayed, seen), (0, 0))
        self.assertIn("vide", out)

    def test_respecte_le_maximum(self):
        api = FauxPubSub([dlq_message(i) for i in range(20)])
        (replayed, seen), _ = self.go(api, max_messages=7)
        self.assertEqual((replayed, seen), (7, 7))
        self.assertEqual(len(api.acked), 7)

    def test_plusieurs_lots(self):
        api = FauxPubSub([dlq_message(i) for i in range(2300)])
        (replayed, _), _ = self.go(api)
        self.assertEqual(replayed, 2300)
        self.assertEqual(len(set(api.acked)), 2300)

    def test_le_resume_n_affiche_pas_les_donnees(self):
        api = FauxPubSub([dlq_message(1, user="alice"), dlq_message(2, user="alice"), dlq_message(3, user="bob")])
        _, out = self.go(api, dry_run=True)
        self.assertIn("alice : 2", out)
        self.assertNotIn(dlq_message(1)["message"]["data"], out)

    def test_cibles_connues(self):
        self.assertEqual(replay.QUEUES["pixel"]["topic"], "pixel-events")
        self.assertNotIn("discord", replay.QUEUES, "discord est refusé : jeton d'interaction et commandes admin")


if __name__ == "__main__":
    unittest.main()
