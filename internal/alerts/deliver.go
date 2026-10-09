package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TelegramAPI is the Bot API base URL (a variable for tests).
var TelegramAPI = "https://api.telegram.org"

var httpClient = &http.Client{Timeout: 10 * time.Second}

// channels returns the configured delivery channels by name.
func channels(s Settings) map[string]func(context.Context, Event) error {
	ch := map[string]func(context.Context, Event) error{}
	if s.WebhookURL != "" {
		ch["webhook"] = func(ctx context.Context, e Event) error { return sendWebhook(ctx, s.WebhookURL, e) }
	}
	if s.NtfyURL != "" {
		ch["ntfy"] = func(ctx context.Context, e Event) error { return sendNtfy(ctx, s.NtfyURL, s.NtfyToken, e) }
	}
	if s.TelegramToken != "" && s.TelegramChat != "" {
		ch["telegram"] = func(ctx context.Context, e Event) error { return sendTelegram(ctx, s.TelegramToken, s.TelegramChat, e) }
	}
	return ch
}

func post(ctx context.Context, url string, body []byte, header map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "llamira")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// sendWebhook POSTs the event as JSON (without the delivery bookkeeping fields).
func sendWebhook(ctx context.Context, url string, e Event) error {
	e.Sent, e.Error, e.Channels = false, "", nil
	body, _ := json.Marshal(struct {
		Event
		Sent     *bool     `json:"sent,omitempty"`
		Error    *string   `json:"error,omitempty"`
		Channels *[]string `json:"channels,omitempty"`
	}{Event: e})
	return post(ctx, url, body, map[string]string{"Content-Type": "application/json"})
}

// sendNtfy publishes to an ntfy topic URL (https://ntfy.sh/<topic> or a self-hosted server).
func sendNtfy(ctx context.Context, url, token string, e Event) error {
	prio := map[string]string{"critical": "urgent", "warning": "high", "info": "default"}[e.Severity]
	tags := map[string]string{"critical": "rotating_light", "warning": "warning", "info": "information_source"}[e.Severity]
	h := map[string]string{
		"Title":    fmt.Sprintf("%s: %s", e.Host, e.Event),
		"Priority": prio,
		"Tags":     tags,
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return post(ctx, url, []byte(e.Message), h)
}

// sendTelegram sends a message through a bot to a chat (user, group or channel id).
func sendTelegram(ctx context.Context, token, chat string, e Event) error {
	icon := map[string]string{"critical": "🚨", "warning": "⚠️", "info": "ℹ️"}[e.Severity]
	body, _ := json.Marshal(map[string]any{
		"chat_id":              chat,
		"text":                 fmt.Sprintf("%s %s · %s\n%s", icon, e.Host, e.Event, e.Message),
		"disable_notification": e.Severity == "info",
	})
	err := post(ctx, TelegramAPI+"/bot"+token+"/sendMessage", body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		// the URL contains the token: never show it in an error
		return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	return nil
}
