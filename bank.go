package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Read-only Alfa API adapter. Credentials and responses never join the AI context.
type BankInput struct {
	Environment string `json:"environment"`
	AuthType    string `json:"authType"`
	Token       string `json:"token"`
	Account     string `json:"account"`
	Date        string `json:"date"`
	Consent     bool   `json:"consent"`
}
type BankRow struct {
	Direction string `json:"direction"`
	Amount    int64  `json:"amountKopecks"`
}
type BankView struct {
	Connected     bool      `json:"connected"`
	Environment   string    `json:"environment,omitempty"`
	AccountMasked string    `json:"accountMasked,omitempty"`
	Date          string    `json:"date,omitempty"`
	Rows          []BankRow `json:"rows"`
	HasNext       bool      `json:"hasNext"`
	Updated       string    `json:"updated,omitempty"`
}
type BankConnection struct {
	mu     sync.Mutex
	config BankInput
	view   BankView
	client *http.Client
}

func newBank() *BankConnection {
	return &BankConnection{view: BankView{Rows: []BankRow{}}, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func kopecks(n json.Number) (int64, error) {
	s := string(n)
	if !regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`).MatchString(s) {
		return 0, fmt.Errorf("unsupported money value")
	}
	parts := strings.Split(s, ".")
	rub, _ := strconv.ParseInt(parts[0], 10, 64)
	minor := int64(0)
	if len(parts) == 2 {
		minor, _ = strconv.ParseInt((parts[1] + "00")[:2], 10, 64)
	}
	return rub*100 + minor, nil
}
func (b *BankConnection) fetch(ctx context.Context, c BankInput) (BankView, error) {
	host := "https://baas.alfabank.ru"
	if c.Environment == "sandbox" {
		host = "https://sandbox.alfabank.ru"
	}
	endpoint := host + "/api/jp/v1/statement/transactions?" + url.Values{"accountNumber": {c.Account}, "statementDate": {c.Date}, "page": {"1"}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	prefix := "ApiKey "
	if c.AuthType == "bearer" {
		prefix = "Bearer "
	}
	req.Header.Set("Authorization", prefix+c.Token)
	req.Header.Set("Accept", "application/json")
	r, err := b.client.Do(req)
	if err != nil {
		return BankView{}, fmt.Errorf("Банк недоступен. Проверьте сеть и доступ Alfa API.")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return BankView{}, fmt.Errorf("Alfa API вернул HTTP %d. Проверьте доступ к transactions, счёт и ключ.", r.StatusCode)
	}
	type amount struct {
		Amount   json.Number `json:"amount"`
		Currency string      `json:"currencyName"`
	}
	var result struct {
		Transactions []struct {
			Amount    *amount `json:"amount"`
			Rub       *amount `json:"amountRub"`
			Direction string  `json:"direction"`
		} `json:"transactions"`
		Links []struct {
			Rel string `json:"rel"`
		} `json:"_links"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.UseNumber()
	if dec.Decode(&result) != nil || result.Transactions == nil {
		return BankView{}, fmt.Errorf("Ответ банка не соответствует формату выписки")
	}
	v := BankView{Connected: true, Environment: c.Environment, AccountMasked: "•••• " + c.Account[len(c.Account)-4:], Date: c.Date, Updated: time.Now().UTC().Format(time.RFC3339), Rows: []BankRow{}}
	for _, t := range result.Transactions {
		a := t.Rub
		if a == nil {
			a = t.Amount
		}
		if a == nil || (a.Currency != "RUR" && a.Currency != "RUB") {
			continue
		}
		n, err := kopecks(a.Amount)
		if err != nil {
			return BankView{}, fmt.Errorf("Неподдерживаемая сумма в ответе банка")
		}
		if t.Direction != "DEBIT" && t.Direction != "CREDIT" {
			return BankView{}, fmt.Errorf("Неизвестное направление операции банка")
		}
		v.Rows = append(v.Rows, BankRow{Direction: t.Direction, Amount: n})
	}
	for _, l := range result.Links {
		if l.Rel == "next" {
			v.HasNext = true
		}
	}
	return v, nil
}
func (a *App) connectBank(w http.ResponseWriter, r *http.Request) {
	var c BankInput
	if !decode(w, r, &c) {
		return
	}
	d, err := time.Parse("2006-01-02", c.Date)
	if !c.Consent || (c.Environment != "sandbox" && c.Environment != "production") || (c.AuthType != "apikey" && c.AuthType != "bearer") || !regexp.MustCompile(`^\d{20}$`).MatchString(c.Account) || len(c.Token) < 8 || len(c.Token) > 8192 || strings.ContainsAny(c.Token, "\r\n") || err != nil || d.After(time.Now().UTC().Add(24*time.Hour)) || d.Year() < 2021 {
		fail(w, 400, "Укажите 20 цифр счёта, дату, действующий ключ и разрешение на чтение выписки")
		return
	}
	v, err := a.bank.fetch(r.Context(), c)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	a.bank.mu.Lock()
	a.bank.config = c
	a.bank.view = v
	a.bank.mu.Unlock()
	send(w, 200, v)
}
func (a *App) bankStatus(w http.ResponseWriter, r *http.Request) {
	a.bank.mu.Lock()
	defer a.bank.mu.Unlock()
	send(w, 200, a.bank.view)
}
func (a *App) disconnectBank(w http.ResponseWriter, r *http.Request) {
	a.bank.mu.Lock()
	a.bank.config = BankInput{}
	a.bank.view = BankView{Rows: []BankRow{}}
	a.bank.mu.Unlock()
	send(w, 200, map[string]bool{"disconnected": true})
}
