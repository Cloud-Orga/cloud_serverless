package drawpixel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestEventTimeUtiliseLHeureDOrigineDUnMessageRejoue(t *testing.T) {
	publie := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	origine := publie.Add(-90 * time.Minute)
	cases := []struct {
		name  string
		attrs map[string]string
		want  time.Time
	}{
		{"message normal", nil, publie},
		{"message rejoué", map[string]string{"original_publish_time": origine.Format(time.RFC3339Nano)}, origine},
		{"heure illisible", map[string]string{"original_publish_time": "hier"}, publie},
		{"heure vide", map[string]string{"original_publish_time": ""}, publie},
		// un message ne peut pas se prétendre plus récent qu'il n'est
		{"heure dans le futur", map[string]string{"original_publish_time": publie.Add(time.Hour).Format(time.RFC3339Nano)}, publie},
	}
	for _, c := range cases {
		if got := eventTime(c.attrs, publie); !got.Equal(c.want) {
			t.Errorf("%s : %v, attendu %v", c.name, got, c.want)
		}
	}
}

func TestLeWorkerOrdonneUnMessageRejoueParSonHeureDOrigine(t *testing.T) {
	os.Setenv("PALETTE_JSON", `{"colors":[{"id":1,"name":"blanc"}]}`)
	f := remplacerSave(t, nil)
	publie := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	origine := publie.Add(-2 * time.Hour)
	data, _ := json.Marshal(map[string]any{"x": 1, "y": 2, "color": 1, "user_id": "u1"})
	body, _ := json.Marshal(map[string]any{"message": map[string]any{
		"data": data, "messageId": "m1", "publishTime": publie,
		"attributes": map[string]string{"original_publish_time": origine.Format(time.RFC3339Nano)},
	}})
	rec := httptest.NewRecorder()
	handle(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || f.calls != 1 {
		t.Fatalf("code %d, appels %d", rec.Code, f.calls)
	}
	if !f.at.Equal(origine) {
		t.Errorf("le pixel est ordonné à %v, attendu l'heure d'origine %v : un rejeu écraserait un pixel plus récent", f.at, origine)
	}
}
