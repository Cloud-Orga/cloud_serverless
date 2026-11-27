package rendergrid

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"
)

// La palette (numéro -> nom, couleur) est dans config/palette.json, commune au back et au front ;
// Terraform la passe dans PALETTE_JSON. Ici on a besoin des couleurs RGB pour dessiner les images :
// c'est la seule fonction du back qui les utilise.
type paletteColor struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

type paletteFile struct {
	Colors []paletteColor `json:"colors"`
}

// loadPalette construit la palette de l'image : l'indice d'un pixel dans l'image EST son numéro de couleur.
// L'indice 0 (case vide) est transparent. Une palette absente ou mal formée est une erreur.
func loadPalette() (color.Palette, error) {
	raw := os.Getenv("PALETTE_JSON")
	if raw == "" {
		return nil, errors.New("PALETTE_JSON absent")
	}
	var f paletteFile
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, fmt.Errorf("PALETTE_JSON illisible: %w", err)
	}
	maxID := 0
	for _, c := range f.Colors {
		if c.ID < 1 || c.ID > 255 {
			return nil, fmt.Errorf("couleur %q : numéro %d hors de 1..255", c.Name, c.ID)
		}
		if c.ID > maxID {
			maxID = c.ID
		}
	}
	if maxID == 0 {
		return nil, errors.New("palette vide")
	}
	// numéros non définis : gris opaque, pour que l'erreur soit visible plutôt qu'une case vide
	pal := make(color.Palette, maxID+1)
	for i := range pal {
		pal[i] = color.NRGBA{R: 128, G: 128, B: 128, A: 255}
	}
	pal[0] = color.NRGBA{} // vide : transparent
	seen := map[int]bool{}
	for _, c := range f.Colors {
		if seen[c.ID] {
			return nil, fmt.Errorf("numéro %d en double", c.ID)
		}
		seen[c.ID] = true
		rgb, err := parseHex(c.Hex)
		if err != nil {
			return nil, fmt.Errorf("couleur %q : %w", c.Name, err)
		}
		pal[c.ID] = rgb
	}
	return pal, nil
}

func parseHex(h string) (color.NRGBA, error) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return color.NRGBA{}, fmt.Errorf("couleur %q : format #RRGGBB attendu", h)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("couleur %q : %w", h, err)
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, nil
}
