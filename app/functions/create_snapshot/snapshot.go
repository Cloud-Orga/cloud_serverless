package createsnapshot

// Logique du snapshot : transformer l'état de Firestore en une image par bloc de 500 x 500 pixels.
// Elle ne connaît ni Firestore ni le bucket (voir store.go) : les tests la font tourner sur des doubles.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"time"
)

// chunkSize : côté d'un bloc, en pixels. C'est un contrat avec draw-pixel et avec le front
// (blocs de 500 x 500, soit 20 x 20 blocs) : le changer demande de prévenir les trois.
const chunkSize = 500

// overlap : on relit un peu avant le dernier instant traité. updated_at est l'heure de publication du message
// (draw-pixel la recopie) : un message rejoué après un échec arrive avec une date ancienne et passerait sinon
// sous le seuil sans jamais figurer dans une image. Relire est sans danger : une image identique n'est pas réécrite.
const overlap = 10 * time.Minute

// pixel : un document pixels/{x}_{y} de Firestore.
type pixel struct {
	X, Y      int
	Color     int
	UpdatedAt time.Time
}

// meta : ce que le snapshot garde entre deux passages, et ce que le front lit pour savoir d'où reprendre l'écoute.
type meta struct {
	// UpdatedUntil : tout pixel modifié jusqu'à cet instant figure dans les images.
	UpdatedUntil time.Time `json:"updated_until"`
	GeneratedAt  time.Time `json:"generated_at"`
}

type pixelSource interface {
	// ChangedSince appelle fn pour chaque pixel modifié après since (tous les pixels si since est nul).
	ChangedSince(ctx context.Context, since time.Time, fn func(pixel)) error
}

type tileStore interface {
	// LoadTile renvoie le PNG du bloc, ou nil s'il n'existe pas encore.
	LoadTile(ctx context.Context, id string) ([]byte, error)
	SaveTile(ctx context.Context, id string, png []byte) error
	// LoadMeta renvoie false si aucun snapshot n'a encore eu lieu.
	LoadMeta(ctx context.Context) (meta, bool, error)
	SaveMeta(ctx context.Context, m meta) error
}

// result : ce qu'un passage a fait, pour les journaux et les tests.
type result struct {
	PixelsRead    int
	TilesTouched  int
	TilesWritten  int
	PixelsSkipped int
	Full          bool
	UpdatedUntil  time.Time
	MetaSaved     bool
}

// chunkID donne le bloc d'un pixel, au format "cx_cy", l'origine 0_0 étant en haut à gauche (comme draw-pixel).
func chunkID(x, y int) string {
	return fmt.Sprintf("%d_%d", x/chunkSize, y/chunkSize)
}

// run fait un passage. Avec full, tout est reconstruit depuis Firestore, sans tenir compte des images existantes
// (rattrapage après une image abîmée ou un changement de palette).
func run(ctx context.Context, src pixelSource, store tileStore, pal color.Palette, full bool, now time.Time) (result, error) {
	res := result{Full: full}

	var since time.Time
	prev, found, err := store.LoadMeta(ctx)
	if err != nil {
		return res, fmt.Errorf("lecture des métadonnées: %w", err)
	}
	if found && !full {
		since = prev.UpdatedUntil.Add(-overlap)
	}

	// 1. pixels modifiés, regroupés par bloc
	byChunk := map[string][]pixel{}
	var latest time.Time
	err = src.ChangedSince(ctx, since, func(p pixel) {
		res.PixelsRead++
		if p.X < 0 || p.Y < 0 || p.Color < 1 || p.Color >= len(pal) {
			res.PixelsSkipped++ // hors grille ou numéro de couleur inconnu de la palette
			return
		}
		id := chunkID(p.X, p.Y)
		byChunk[id] = append(byChunk[id], p)
		if p.UpdatedAt.After(latest) {
			latest = p.UpdatedAt
		}
	})
	if err != nil {
		return res, fmt.Errorf("lecture des pixels: %w", err)
	}

	// 2. un bloc par groupe : image existante + pixels modifiés, réécrite seulement si elle change
	for id, pixels := range byChunk {
		res.TilesTouched++
		old, err := store.LoadTile(ctx, id)
		if err != nil {
			return res, fmt.Errorf("lecture du bloc %s: %w", id, err)
		}
		img := blankTile(pal)
		if old != nil && !full {
			if decoded, ok := decodeTile(old); ok {
				img = decoded
				img.Palette = pal // les indices sont des numéros de couleur : la palette courante s'applique
			} else {
				logger.Warn("bloc illisible, reconstruit depuis les pixels lus : lancer un passage complet", "tile", id)
			}
		}
		for _, p := range pixels {
			img.SetColorIndex(p.X%chunkSize, p.Y%chunkSize, uint8(p.Color))
		}
		data, err := encodeTile(img)
		if err != nil {
			return res, fmt.Errorf("encodage du bloc %s: %w", id, err)
		}
		if bytes.Equal(data, old) {
			continue // rien n'a changé dans ce bloc
		}
		if err := store.SaveTile(ctx, id, data); err != nil {
			return res, fmt.Errorf("écriture du bloc %s: %w", id, err)
		}
		res.TilesWritten++
	}

	// 3. métadonnées : seulement si le seuil avance (chaque écriture crée une version dans le bucket)
	res.UpdatedUntil = prev.UpdatedUntil
	if !latest.IsZero() && (latest.After(prev.UpdatedUntil) || full) {
		res.UpdatedUntil = latest
		if err := store.SaveMeta(ctx, meta{UpdatedUntil: latest, GeneratedAt: now}); err != nil {
			return res, fmt.Errorf("écriture des métadonnées: %w", err)
		}
		res.MetaSaved = true
	}
	return res, nil
}

func blankTile(pal color.Palette) *image.Paletted {
	return image.NewPaletted(image.Rect(0, 0, chunkSize, chunkSize), pal)
}

// decodeTile relit un bloc écrit par encodeTile. Une image d'une autre forme n'est pas un bloc valide.
func decodeTile(data []byte) (*image.Paletted, bool) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	p, ok := img.(*image.Paletted)
	if !ok || p.Bounds() != image.Rect(0, 0, chunkSize, chunkSize) {
		return nil, false
	}
	return p, true
}

// encodeTile : PNG à palette (un octet par pixel), compression maximale : un bloc presque vide pèse très peu.
func encodeTile(img *image.Paletted) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
