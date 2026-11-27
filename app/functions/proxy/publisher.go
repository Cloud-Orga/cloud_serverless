package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
)

var (
	clientOnce sync.Once
	client     *pubsub.Client
	clientErr  error

	publishersMu sync.Mutex
	publishers   = map[string]*pubsub.Publisher{}
)

// getPublisher crée le client au premier appel, puis garde un publisher par topic (instance chaude).
func getPublisher(topic string) (*pubsub.Publisher, error) {
	clientOnce.Do(func() {
		// context.Background : le client doit survivre à la requête qui le crée.
		client, clientErr = pubsub.NewClient(context.Background(), os.Getenv("GCP_PROJECT"))
	})
	if clientErr != nil {
		return nil, clientErr
	}

	publishersMu.Lock()
	defer publishersMu.Unlock()
	if p, ok := publishers[topic]; ok {
		return p, nil
	}
	p := client.Publisher(topic)
	publishers[topic] = p
	return p, nil
}

// warmUp prépare le client Pub/Sub dès le démarrage de l'instance, avant la première requête :
// création du client, jeton d'accès, connexion à Google. Sans cela, le premier envoi après chaque démarrage
// paie tout cela en plus (mesuré : environ 0,2 s de plus sur le premier appel).
// L'appel GetTopic sert seulement à ouvrir la connexion ; le compte du proxy n'a que le droit de publier,
// donc il est refusé, et c'est sans importance : la connexion et le jeton restent chauds.
func warmUp() {
	project := os.Getenv("GCP_PROJECT")
	if project == "" {
		return // tests et exécution locale sans configuration
	}
	start := time.Now()
	for _, name := range []string{os.Getenv("TOPIC_NAME"), os.Getenv("DISCORD_TOPIC_NAME")} {
		if name == "" {
			continue
		}
		if _, err := getPublisher(name); err != nil {
			logger.Warn("préchauffage pubsub impossible", "error", err.Error())
			return
		}
	}
	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = client.TopicAdminClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{
			Topic: "projects/" + project + "/topics/" + os.Getenv("TOPIC_NAME"),
		})
	}
	logger.Info("pubsub préchauffé", "ms", time.Since(start).Milliseconds())
}

func init() {
	go warmUp()
}

// publishMessage envoie un message sur un topic et attend l'accusé de Pub/Sub.
func publishMessage(ctx context.Context, topic string, data []byte, attrs map[string]string) error {
	pub, err := getPublisher(topic)
	if err != nil {
		return fmt.Errorf("client pubsub: %w", err)
	}
	res := pub.Publish(ctx, &pubsub.Message{Data: data, Attributes: attrs})
	_, err = res.Get(ctx)
	return err
}

// publishDraw envoie l'événement pixel.draw (chemin web).
func publishDraw(ctx context.Context, evt drawEvent, eventID, correlationID string) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return publishMessage(ctx, os.Getenv("TOPIC_NAME"), data, map[string]string{
		"type":           "pixel.draw",
		"event_id":       eventID,
		"correlation_id": correlationID,
	})
}

// publishCommand envoie l'événement discord.command (chemin Discord).
func publishCommand(ctx context.Context, cmd discordCommand, eventID, correlationID string) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return publishMessage(ctx, os.Getenv("DISCORD_TOPIC_NAME"), data, map[string]string{
		"type":           "discord.command",
		"command":        cmd.Command,
		"event_id":       eventID,
		"correlation_id": correlationID,
	})
}

// publish et publishDiscord sont des variables pour que les tests remplacent la vraie publication.
var (
	publish        = publishDraw
	publishDiscord = publishCommand
)
