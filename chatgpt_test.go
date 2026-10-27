package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func responseJSON(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}
}
func signingKey(t *testing.T) (*rsa.PrivateKey, jwk) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, jwk{Kty: "RSA", Kid: "test-key", Alg: "RS256", Use: "sig", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: "AQAB"}
}
func signedID(t *testing.T, key *rsa.PrivateKey, nonce string, changes map[string]any) string {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	claims := map[string]any{"iss": "https://auth.openai.com", "sub": "test-subject", "aud": "oaiapp_test", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "email": "test@example.invalid"}
	for k, v := range changes {
		claims[k] = v
	}
	b, _ := json.Marshal(claims)
	content := h + "." + base64.RawURLEncoding.EncodeToString(b)
	digest := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return content + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func TestIDTokenValidation(t *testing.T) {
	key, j := signingKey(t)
	good := signedID(t, key, "nonce", nil)
	if _, err := verifyJWT(good, "oaiapp_test", "nonce", []jwk{j}); err != nil {
		t.Fatal(err)
	}
	for _, changes := range []map[string]any{{"iss": "https://evil.example"}, {"aud": "different-client"}, {"exp": time.Now().Add(-time.Hour).Unix()}, {"nonce": "different"}, {"sub": ""}, {"iat": time.Now().Add(time.Hour).Unix()}} {
		if _, err := verifyJWT(signedID(t, key, "nonce", changes), "oaiapp_test", "nonce", []jwk{j}); err == nil {
			t.Fatal("invalid claims accepted", changes)
		}
	}
	if _, err := verifyJWT(good[:len(good)-10]+"aaaaaaaaaa", "oaiapp_test", "nonce", []jwk{j}); err == nil {
		t.Fatal("tampered signature accepted")
	}
	if _, err := verifyJWT("not.a.jwt", "oaiapp_test", "nonce", []jwk{j}); err == nil {
		t.Fatal("invalid JWT accepted")
	}
}
func TestResponsesStreamRequiresCompletion(t *testing.T) {
	valid := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Привет\"}\n\ndata: {\"type\":\"response.completed\"}\n"
	out, err := readResponseStream(strings.NewReader(valid))
	if err != nil || out != "Привет" {
		t.Fatal(out, err)
	}
	for _, s := range []string{"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n", "data: {\"type\":\"response.failed\"}\n", "data: {\"type\":\"response.completed\"}\n"} {
		if _, err := readResponseStream(strings.NewReader(s)); err == nil {
			t.Fatal("unfinished response accepted")
		}
	}
}
func TestChatGPTAuthorizationAndInference(t *testing.T) {
	key, j := signingKey(t)
	a := testApp(t)
	c := a.auth
	var tokenCalls int
	var nonce string
	c.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			return responseJSON(oidcDoc{Issuer: "https://auth.openai.com", Authorize: "https://auth.openai.com/api/accounts/authorize", Token: "https://auth.openai.com/api/accounts/oauth/token", JWKS: "https://auth.openai.com/.well-known/jwks.json"}), nil
		case "/api/accounts/oauth/token":
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("resource") != "https://api.openai.com/v1" {
				t.Error("incorrect token parameters")
			}
			tokenCalls++
			return responseJSON(tokenSet{Access: "test-access", Refresh: "test-refresh", ID: signedID(t, key, nonce, nil), Type: "Bearer", Scope: "openid chatgpt.tokens.use.direct", ExpiresIn: 3600}), nil
		case "/.well-known/jwks.json":
			return responseJSON(map[string]any{"keys": []jwk{j}}), nil
		case "/v1/models":
			return responseJSON(map[string]any{"models": []catalogModel{{Slug: "available", Name: "Available", Visibility: "list"}, {Slug: "hidden", Name: "Hidden", Visibility: "hide"}}}), nil
		case "/v1/responses":
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["store"] != false || payload["stream"] != true || payload["model"] != "available" {
				t.Error("incorrect Responses parameters")
			}
			if r.Header.Get("Authorization") != "Bearer test-access" {
				t.Error("wrong access token")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Данные получены.\"}\n\ndata: {\"type\":\"response.completed\"}\n")), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
			return nil, nil
		}
	})
	_ = tokenCalls
	start := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/chatgpt/start", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, start)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var started map[string]string
	json.Unmarshal(w.Body.Bytes(), &started)
	u, _ := url.Parse(started["url"])
	if u.Query().Get("client_id") != "dynamic_agent_client" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE registration")
	}
	attempt := *c.pending
	// The transport closure needs the original nonce after the callback consumes its attempt.
	nonce = attempt.Nonce

	callback := httptest.NewRequest("GET", "http://127.0.0.1:8080/auth/callback?state="+url.QueryEscape(attempt.State)+"&code=test-code&client_id=oaiapp_test", nil)
	callback.AddCookie(w.Result().Cookies()[0])
	cw := httptest.NewRecorder()
	a.handler().ServeHTTP(cw, callback)
	if cw.Code != 303 || !c.connected() {
		t.Fatal("authorization failed", cw.Code, c.lastError)
	}
	if tokenCalls != 1 {
		t.Fatal("code exchanged more than once")
	}
	again := httptest.NewRecorder()
	a.handler().ServeHTTP(again, callback)
	if again.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	sel := request(a, "POST", "/api/chatgpt/select", `{"model":"available"}`)
	if sel.Code != 200 {
		t.Fatal(sel.Code, sel.Body)
	}
	res := request(a, "POST", "/api/chat", `{"message":"Что делать?"}`)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"mode":"chatgpt"`) {
		t.Fatal(res.Code, res.Body)
	}
	for _, route := range []string{"/api/health", "/api/chatgpt/status"} {
		status := request(a, "GET", route, "")
		if strings.Contains(status.Body.String(), "test-access") || strings.Contains(status.Body.String(), "test-refresh") {
			t.Fatal("OAuth secret leaked")
		}
	}
}
