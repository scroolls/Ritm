package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *App) aiConfig() Config { a.configMu.RLock(); defer a.configMu.RUnlock(); return a.config }
func (a *App) configureAI(w http.ResponseWriter, r *http.Request) {
	var b struct {
		BaseURL  string `json:"baseURL"`
		Model    string `json:"model"`
		Key      string `json:"key"`
		Provider string `json:"provider"`
	}
	if !decode(w, r, &b) {
		return
	}
	b.BaseURL = strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	b.Model = strings.TrimSpace(b.Model)
	b.Key = strings.TrimSpace(b.Key)
	if b.Provider == "" {
		b.Provider = "api"
	}
	if b.Provider != "api" && b.Provider != "ollama" {
		fail(w, 400, "Неизвестный провайдер")
		return
	}
	if b.Provider == "ollama" {
		b.BaseURL = "http://127.0.0.1:11434/v1"
		b.Key = "local-ollama"
	}
	u, err := url.Parse(b.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) || len(b.Model) > 200 || len(b.Model) == 0 || len(b.Key) < 8 || len(b.Key) > 1000 {
		fail(w, 400, "Укажите HTTPS-адрес API, модель и ключ. Локальный HTTP разрешён только для localhost.")
		return
	}
	select {
	case a.aiSlots <- struct{}{}:
		defer func() { <-a.aiSlots }()
	default:
		fail(w, 429, "Помощник занят. Повторите позже.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"model": b.Model, "messages": []Message{{Role: "user", Content: "Ответь одним словом: Готово"}}, "stream": false})
	req, err := http.NewRequestWithContext(ctx, "POST", b.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		fail(w, 400, "Проверьте адрес API")
		return
	}
	req.Header.Set("Authorization", "Bearer "+b.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		fail(w, 502, "Не удалось связаться с провайдером. Проверьте адрес и сеть.")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fail(w, 502, "Провайдер отклонил проверку. Проверьте ключ, модель и доступ к API.")
		return
	}
	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil || len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		fail(w, 502, "Модель вернула пустой или неподдерживаемый ответ")
		return
	}
	a.configMu.Lock()
	a.config = Config{BaseURL: b.BaseURL, Model: b.Model, Key: b.Key, Provider: b.Provider}
	a.configMu.Unlock()
	send(w, 200, map[string]any{"configured": true, "model": b.Model})
}
