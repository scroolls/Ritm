package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type rateEntry struct {
	Count int
	Start time.Time
}
type rateGate struct {
	mu      sync.Mutex
	entries map[string]rateEntry
}

func (g *rateGate) allow(key string, limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.entries == nil {
		g.entries = map[string]rateEntry{}
	}
	now := time.Now()
	if len(g.entries) > 1024 {
		for k, e := range g.entries {
			if now.Sub(e.Start) > time.Minute {
				delete(g.entries, k)
			}
		}
		if len(g.entries) > 1024 {
			return false
		}
	}
	e := g.entries[key]
	if now.Sub(e.Start) > time.Minute {
		e = rateEntry{Start: now}
	}
	e.Count++
	g.entries[key] = e
	return e.Count <= limit
}
func (a *App) authorized(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	public := strings.ToLower(os.Getenv("PUBLIC_HOST"))
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && (public == "" || host != public) {
		fail(w, 403, "Адрес сайта не разрешён")
		return false
	}
	if password := os.Getenv("APP_PASSWORD"); password != "" {
		user, p, ok := r.BasicAuth()
		actual := sha256.Sum256([]byte(p))
		expected := sha256.Sum256([]byte(password))
		if !ok || user != env("APP_USER", "owner") || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Ritm", charset="UTF-8"`)
			fail(w, 401, "Требуется вход владельца")
			return false
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		remote, _, _ := net.SplitHostPort(r.RemoteAddr)
		key := remote + ":general"
		limit := 240
		if r.URL.Path == "/api/chat" {
			key = remote + ":chat"
			limit = 12
		}
		if strings.HasPrefix(r.URL.Path, "/api/chatgpt/start") || r.URL.Path == "/api/ai/config" || r.URL.Path == "/api/bank/connect" {
			key = remote + ":connect"
			limit = 12
		}
		if !a.gate.allow(key, limit) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "Слишком много запросов. Повторите через минуту.")
			return false
		}
	}
	return true
}
