package rendergrid

// render-grid : répond aux commandes Discord /get et /snapshot.
// Lu depuis la souscription discord-command-grid (filtre : command = get ou snapshot). Il assemble la grille
// depuis les blocs du bucket privé, puis répond dans Discord en modifiant le message « réfléchit… ».

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"net/http"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

// maxAttachment : au-delà, l'image n'est pas jointe à la réponse (limite de téléversement de Discord) mais
// donnée par un lien signé. 8 Mio est la limite la plus basse d'un serveur Discord. Variable pour les tests.
var maxAttachment = 8 << 20

const (
	// linkTTL : durée de validité d'un lien signé
	linkTTL = time.Hour

	// maxAttempts : voir dead_letter_policy dans messaging.tf. À la dernière tentative, on prévient l'utilisateur
	// au lieu de le laisser devant « L'application ne répond plus ».
	maxAttempts = 5
)

// pushEnvelope : l'enveloppe d'une souscription push ; deliveryAttempt est présent parce que la souscription
// a une file d'erreurs.
type pushEnvelope struct {
	Message struct {
		Data       []byte            `json:"data"`
		Attributes map[string]string `json:"attributes"`
		MessageID  string            `json:"messageId"`
	} `json:"message"`
	DeliveryAttempt int `json:"deliveryAttempt"`
}

// command : l'événement discord.command publié par discord-proxy.
type command struct {
	InteractionID string `json:"interaction_id"`
	ApplicationID string `json:"application_id"`
	Token         string `json:"token"` // sert à répondre dans Discord : ne jamais le journaliser
	Command       string `json:"command"`
	UserID        string `json:"user_id"`
}

// deps : ce dont la logique a besoin, remplaçable dans les tests.
type deps struct {
	store objectStore
	pal   color.Palette
	reply func(ctx context.Context, appID, token, content string, file []byte, filename string) error
}

func init() {
	functions.HTTP("RenderGrid", handle)
}

// buildDeps est une variable pour que les tests remplacent le vrai bucket et le vrai Discord.
var buildDeps = defaultDeps

func defaultDeps() (deps, error) {
	pal, err := loadPalette()
	if err != nil {
		return deps{}, fmt.Errorf("palette: %w", err)
	}
	client, err := getClient()
	if err != nil {
		return deps{}, fmt.Errorf("client bucket: %w", err)
	}
	return deps{store: bucketStore{client.Bucket(os.Getenv("BUCKET_NAME"))}, pal: pal, reply: reply}, nil
}

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

	var cmd command
	if err := json.Unmarshal(env.Message.Data, &cmd); err != nil {
		// un message illisible ne guérira pas en réessayant : 400 l'envoie vite en file d'erreurs
		logger.Warn("message illisible", "message_id", messageID, "correlation_id", correlationID, "error", err.Error())
		http.Error(w, "message illisible", http.StatusBadRequest)
		return
	}

	d, err := buildDeps()
	if err != nil {
		logger.Error("dépendances indisponibles", "message_id", messageID, "error", err.Error())
		http.Error(w, "dépendances indisponibles", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()

	start := time.Now()
	res, err := process(ctx, d, cmd, start.UTC())
	if err != nil {
		logger.Error("commande échouée", "message_id", messageID, "correlation_id", correlationID,
			"command", cmd.Command, "attempt", env.DeliveryAttempt, "error", err.Error())
		if env.DeliveryAttempt >= maxAttempts {
			// dernière tentative : on le dit à l'utilisateur, puis on acquitte (le message ne partira pas en file d'erreurs)
			msg := "Impossible de générer l'image de la grille pour le moment. Réessayez dans quelques minutes."
			if rerr := d.reply(ctx, cmd.ApplicationID, cmd.Token, msg, nil, ""); rerr != nil {
				logger.Error("réponse d'erreur impossible", "message_id", messageID, "error", rerr.Error())
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "commande échouée", http.StatusInternalServerError)
		return
	}
	logger.Info("commande traitée", "message_id", messageID, "correlation_id", correlationID, "command", cmd.Command,
		"user_id", cmd.UserID, "mode", res.Mode, "bytes", res.Bytes, "object", res.Object, "ms", time.Since(start).Milliseconds())
	w.WriteHeader(http.StatusOK)
}

// result : ce qu'une commande a fait, pour les journaux et les tests.
type result struct {
	Mode   string // "attachment", "link"
	Bytes  int
	Object string
}

// process exécute une commande /get ou /snapshot.
func process(ctx context.Context, d deps, cmd command, now time.Time) (result, error) {
	if cmd.Command != "get" && cmd.Command != "snapshot" {
		return result{}, fmt.Errorf("commande non gérée : %q", cmd.Command)
	}

	img, err := assemble(ctx, d.store, d.pal)
	if err != nil {
		return result{}, fmt.Errorf("assemblage: %w", err)
	}
	data, err := encode(img)
	if err != nil {
		return result{}, fmt.Errorf("encodage: %w", err)
	}
	m, found, err := d.store.LoadMeta(ctx)
	if err != nil {
		return result{}, fmt.Errorf("métadonnées: %w", err)
	}
	fresh := "La grille est vide."
	if found {
		fresh = fmt.Sprintf("Dernier pixel pris en compte : %s UTC (les snapshots ont lieu toutes les 5 minutes).",
			m.UpdatedUntil.UTC().Format("02/01 15:04:05"))
	}
	stamp := now.UTC().Format("20060102T150405Z")

	// /get : l'image jointe à la réponse quand elle tient, sinon un lien (rangé 1 jour, voir le cycle de vie du bucket)
	if cmd.Command == "get" && len(data) <= maxAttachment {
		if err := d.reply(ctx, cmd.ApplicationID, cmd.Token, "Grille actuelle. "+fresh, data, "grille-"+stamp+".png"); err != nil {
			return result{}, fmt.Errorf("réponse Discord: %w", err)
		}
		return result{Mode: "attachment", Bytes: len(data)}, nil
	}

	// /snapshot : toujours rangé, avec un nom daté (l'historique) ; /get trop gros : rangé sous get/
	prefix := "snapshots/"
	if cmd.Command == "get" {
		prefix = "get/"
	}
	name := prefix + stamp + ".png"
	if err := d.store.Put(ctx, name, "image/png", data); err != nil {
		return result{}, fmt.Errorf("écriture de %s: %w", name, err)
	}
	link, err := d.store.SignedURL(ctx, name, linkTTL)
	if err != nil {
		return result{}, fmt.Errorf("lien signé: %w", err)
	}
	what := "Snapshot enregistré"
	if cmd.Command == "get" {
		what = "L'image de la grille est trop lourde pour être jointe"
	}
	content := fmt.Sprintf("%s. %s\nLien valable 1 heure : %s", what, fresh, link)
	if err := d.reply(ctx, cmd.ApplicationID, cmd.Token, content, nil, ""); err != nil {
		return result{}, fmt.Errorf("réponse Discord: %w", err)
	}
	return result{Mode: "link", Bytes: len(data), Object: name}, nil
}
