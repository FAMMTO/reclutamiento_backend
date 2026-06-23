// Package email envía correos transaccionales vía la API REST de Resend.
// No usa el SDK oficial para mantener dependencias mínimas.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Sender struct {
	apiKey string
	from   string
	client *http.Client
}

func New(apiKey, from string) *Sender {
	return &Sender{
		apiKey: apiKey,
		from:   from,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type message struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

func (s *Sender) send(ctx context.Context, to, subject, html string) error {
	data, err := json.Marshal(message{From: s.from, To: []string{to}, Subject: subject, HTML: html})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("resend: status %d", resp.StatusCode)
	}
	return nil
}

func (s *Sender) SendPasswordReset(ctx context.Context, to, name, resetURL string) error {
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="es">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background:#f4f4f5;font-family:system-ui,-apple-system,sans-serif">
  <div style="max-width:520px;margin:40px auto;background:#fff;border-radius:12px;overflow:hidden;border:1px solid #e4e4e7">
    <div style="background:#1a1a2e;padding:28px 32px">
      <span style="color:#fff;font-size:20px;font-weight:700;letter-spacing:-0.5px">Jobbly</span>
    </div>
    <div style="padding:32px">
      <p style="margin:0 0 8px;font-size:22px;font-weight:700;color:#09090b">Restablece tu contraseña</p>
      <p style="margin:0 0 24px;font-size:14px;color:#71717a">Hola %s, recibimos una solicitud para restablecer tu contraseña. El enlace expira en 30 minutos.</p>
      <a href="%s" style="display:inline-block;background:#1a1a2e;color:#fff;text-decoration:none;padding:12px 24px;border-radius:8px;font-size:14px;font-weight:600">Restablecer contraseña</a>
      <p style="margin:24px 0 0;font-size:12px;color:#a1a1aa">Si no solicitaste este cambio, ignora este correo. Tu contraseña no cambiará.</p>
      <p style="margin:8px 0 0;font-size:12px;color:#a1a1aa">¿El botón no funciona? Copia y pega esta URL en tu navegador:<br>
        <span style="color:#3b82f6;word-break:break-all">%s</span></p>
    </div>
  </div>
</body>
</html>`, name, resetURL, resetURL)

	return s.send(ctx, to, "Restablece tu contraseña — Jobbly", html)
}
