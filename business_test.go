package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaxMathAndForecast(t *testing.T) {
	c := defaultTax()
	e := estimateTax(c)
	if e.Gross != 288000 || e.Deduction != 90000 || e.Payable != 66000 || e.Due != "2026-10-28" {
		t.Fatal(e)
	}
	c.Contributions = 200000
	if estimateTax(c).Deduction != 144000 {
		t.Fatal("employee deduction cap missing")
	}
	c.Employees = false
	if estimateTax(c).Deduction != 200000 {
		t.Fatal("wrong no-employee cap")
	}
	a := testApp(t)
	base := dashboard(a.snapshot(), 30, 0)
	c = defaultTax()
	c.PriorAdvances = 140000
	b, _ := json.Marshal(c)
	w := request(a, "POST", "/api/tax", string(b))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	d := dashboard(a.snapshot(), 30, 0)
	if d.Forecast[13].Balance != base.Forecast[13].Balance+8000 {
		t.Fatal("tax forecast not updated")
	}
	request(a, "POST", "/api/payments/tax/pay", "{}")
	if request(a, "POST", "/api/tax", string(b)).Code != 409 {
		t.Fatal("paid tax silently rewritten")
	}
}
func TestDeleteExpenseRestoresBalance(t *testing.T) {
	a := testApp(t)
	w := request(a, "POST", "/api/expenses", `{"name":"Упаковка","category":"Закупки","amount":3500,"date":"2026-10-27"}`)
	var e Expense
	json.Unmarshal(w.Body.Bytes(), &e)
	if request(a, "DELETE", "/api/expenses/"+e.ID, "").Code != 200 || len(a.snapshot().Expenses) != 0 {
		t.Fatal("delete failed")
	}
	if request(a, "DELETE", "/api/expenses/"+e.ID, "").Code != 404 {
		t.Fatal("missing expense accepted")
	}
}
func TestNamesRejectMarkup(t *testing.T) {
	a := testApp(t)
	for _, name := range []string{"Упаковка <b>крафт</b>", "<img src=x onerror=alert(1)>", "x\ny", "x\ty"} {
		b, _ := json.Marshal(Expense{Name: name, Category: "Прочее", Amount: 1, Date: demoDate})
		if request(a, "POST", "/api/expenses", string(b)).Code != 400 {
			t.Fatal("markup allowed", name)
		}
	}
}
func TestAlfaReadOnlyAdapterAndSecretIsolation(t *testing.T) {
	a := testApp(t)
	a.bank.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "baas.alfabank.ru" || r.URL.Path != "/api/jp/v1/statement/transactions" || r.Header.Get("Authorization") != "ApiKey bank-test-secret" || r.URL.Query().Get("accountNumber") != "00000000000000000000" {
			t.Fatal("wrong bank request")
		}
		return responseJSON(map[string]any{"transactions": []any{map[string]any{"amountRub": map[string]any{"amount": 100.01, "currencyName": "RUR"}, "direction": "DEBIT"}}, "_links": []any{map[string]string{"rel": "next"}}}), nil
	})
	b := `{"environment":"production","authType":"apikey","token":"bank-test-secret","account":"00000000000000000000","date":"2026-10-27","consent":true}`
	w := request(a, "POST", "/api/bank/connect", b)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var v BankView
	json.Unmarshal(w.Body.Bytes(), &v)
	if len(v.Rows) != 1 || v.Rows[0].Amount != 10001 || !v.HasNext {
		t.Fatal("money precision", v)
	}
	status := request(a, "GET", "/api/bank/status", "").Body.String()
	if strings.Contains(status, "bank-test-secret") || strings.Contains(status, "00000000000000000000") {
		t.Fatal("bank secrets exposed")
	}
	if request(a, "POST", "/api/bank/disconnect", "{}").Code != 200 || a.bank.view.Connected {
		t.Fatal("bank disconnect failed")
	}
	for _, s := range []string{"100.001", "-2", "1e3", "NaN"} {
		if _, err := kopecks(json.Number(s)); err == nil {
			t.Fatal("imprecise amount accepted", s)
		}
	}
}
func TestBankConnectionRequiresConsent(t *testing.T) {
	a := testApp(t)
	if request(a, "POST", "/api/bank/connect", `{"environment":"production","authType":"apikey","token":"test-secret","account":"00000000000000000000","date":"2026-10-27","consent":false}`).Code != 400 {
		t.Fatal("consent missing")
	}
}
func TestHostAndPasswordProtection(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("POST", "http://attacker.example/api/reset", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
	t.Setenv("APP_PASSWORD", "example-password-for-test")
	t.Setenv("APP_USER", "owner")
	if request(a, "GET", "/api/state", "").Code != 401 {
		t.Fatal("missing password accepted")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:8080/api/state", nil)
	r.SetBasicAuth("owner", "example-password-for-test")
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("valid owner denied")
	}
}
func TestChatRateLimit(t *testing.T) {
	a := testApp(t)
	for i := 0; i < 12; i++ {
		if w := request(a, "POST", "/api/chat", `{"message":"Привет"}`); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if w := request(a, "POST", "/api/chat", `{"message":"Привет"}`); w.Code != 429 {
		t.Fatal("unlimited model requests")
	}
}
