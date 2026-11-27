package createsnapshot

// create-snapshot : lu depuis la souscription snapshot (Cloud Scheduler publie toutes les 5 minutes).
// Il met à jour les images des blocs modifiés dans le bucket privé.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

type pushEnvelope struct {
	Message struct {
		Data       []byte            `json:"data"`
		Attributes map[string]string `json:"attributes"`
		MessageID  string            `json:"messageId"`
	} `json:"message"`
}

// trigger : le contenu du message. Un message vide (le planificateur) veut dire « passage normal ».
type trigger struct {
	// Full reconstruit toutes les images depuis Firestore (rattrapage). À publier à la main sur le topic.
	Full bool `json:"full"`
}

func init() {
	functions.HTTP("CreateSnapshot", handle)
}

// Un seul passage à la fois dans l'instance (et le plafond d'instances est 1) : deux passages simultanés
// pourraient écrire le même bloc avec des états différents.
var runMu sync.Mutex

func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST uniquement", http.StatusMethodNotAllowed)
		return
	}

	var env pushEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		logger.Warn("enveloppe invalide", "error", err.Error())
		http.Error(w, "enveloppe invalide", http.StatusBadRequest)
		return
	}
	messageID := env.Message.MessageID
	correlationID := env.Message.Attributes["correlation_id"]

	var t trigger
	if len(env.Message.Data) > 0 {
		// le contenu du planificateur n'est pas un déclencheur de rattrapage : on ignore ce qu'on ne comprend pas
		_ = json.Unmarshal(env.Message.Data, &t)
	}

	pal, err := loadPalette()
	if err != nil {
		// erreur de configuration : réessayer ne la corrigera pas, mais on ne dessine pas avec une palette inconnue
		logger.Error("palette inutilisable", "message_id", messageID, "error", err.Error())
		http.Error(w, "palette inutilisable", http.StatusInternalServerError)
		return
	}

	fs, gcs, err := getClients()
	if err != nil {
		logger.Error("clients indisponibles", "message_id", messageID, "error", err.Error())
		http.Error(w, "clients indisponibles", http.StatusInternalServerError)
		return
	}

	runMu.Lock()
	defer runMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()

	start := time.Now()
	res, err := run(ctx, firestoreSource{fs}, bucketStore{gcs.Bucket(os.Getenv("BUCKET_NAME"))}, pal, t.Full, start.UTC())
	if err != nil {
		// 500 : Pub/Sub réessaie, puis envoie le message dans la DLQ après 5 échecs
		logger.Error("snapshot échoué", "message_id", messageID, "correlation_id", correlationID, "error", err.Error())
		http.Error(w, "snapshot échoué", http.StatusInternalServerError)
		return
	}
	logger.Info("snapshot terminé", "message_id", messageID, "correlation_id", correlationID,
		"full", res.Full, "pixels_read", res.PixelsRead, "pixels_skipped", res.PixelsSkipped,
		"tiles_touched", res.TilesTouched, "tiles_written", res.TilesWritten,
		"updated_until", res.UpdatedUntil, "ms", time.Since(start).Milliseconds())
	w.WriteHeader(http.StatusOK)
}
