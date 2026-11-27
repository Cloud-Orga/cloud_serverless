package rendergrid

// Réponse à Discord après l'accusé différé du proxy : on modifie le message « réfléchit… » avec le jeton de
// l'interaction (valable 15 minutes). Aucun jeton de bot n'est nécessaire, et le jeton d'interaction ne
// doit jamais apparaître dans les journaux.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"time"
)

// discordAPI est une variable pour que les tests visent un faux serveur.
var discordAPI = "https://discord.com/api/v10"

var httpClient = &http.Client{Timeout: 25 * time.Second}

// reply remplace le message d'attente. Avec file non vide, l'image est jointe.
func reply(ctx context.Context, appID, token, content string, file []byte, filename string) error {
	if appID == "" || token == "" {
		return fmt.Errorf("application_id ou jeton d'interaction manquant")
	}
	endpoint := fmt.Sprintf("%s/webhooks/%s/%s/messages/@original", discordAPI, url.PathEscape(appID), url.PathEscape(token))

	var body bytes.Buffer
	contentType := "application/json"
	if len(file) == 0 {
		if err := json.NewEncoder(&body).Encode(map[string]any{"content": content}); err != nil {
			return err
		}
	} else {
		mw := multipart.NewWriter(&body)
		payload, _ := json.Marshal(map[string]any{
			"content":     content,
			"attachments": []map[string]any{{"id": 0, "filename": filename}},
		})
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="payload_json"`)
		h.Set("Content-Type", "application/json")
		part, _ := mw.CreatePart(h)
		_, _ = part.Write(payload)
		fh := textproto.MIMEHeader{}
		fh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[0]"; filename=%q`, filename))
		fh.Set("Content-Type", "image/png")
		fp, _ := mw.CreatePart(fh)
		_, _ = fp.Write(file)
		_ = mw.Close()
		contentType = mw.FormDataContentType()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := httpClient.Do(req)
	if err != nil {
		// l'URL contient le jeton : on ne renvoie que la cause réseau, sans l'adresse
		return fmt.Errorf("discord injoignable: %s", urlErrorCause(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("discord a répondu %d: %s", resp.StatusCode, detail)
	}
	return nil
}

func urlErrorCause(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
