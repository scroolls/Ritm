package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	a, err := newApp(filepath.Join(t.TempDir(), "state.json"), Config{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func request(a *App, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}
func TestForecastConservation(t *testing.T) {
	payments := paymentContext(initialState().Payments)
	if payments[0]["date"] != "2026-10-29" || payments[3]["date"] != "2026-11-08" {
		t.Fatal("ambiguous calendar dates")
	}
	s := initialState()
	pts := forecast(s, 0)
	if len(pts) != 14 {
		t.Fatal("expected 14 days")
	}
	balance := s.Balance
	for _, p := range pts {
		balance += p.Income - p.Outflow
		if p.Balance != balance {
			t.Fatal("cash balance violated")
		}
	}
	d := dashboard(s, 30, 0)
	if d.Gap != 24800 || d.GapDate != "2026-11-08" || d.Score != 50.7 {
		t.Fatalf("unexpected baseline: %+v", d)
	}
	optimistic := dashboard(s, 30, 15)
	if optimistic.Gap != 0 || optimistic.Score != 69.3 {
		t.Fatalf("optimistic scenario: %+v", optimistic)
	}
}
func TestPaidObligationNeverDoubleCounts(t *testing.T) {
	s := initialState()
	base := forecast(s, 0)
	for i := range s.Payments {
		s.Payments[i].Paid = true
	}
	paid := forecast(s, 0)
	if base[13].Balance != paid[13].Balance {
		t.Fatalf("double counting: %d vs %d", base[13].Balance, paid[13].Balance)
	}
}
func TestExpensesChangeBothBalanceAndProfit(t *testing.T) {
	s := initialState()
	b := dashboard(s, 30, 0)
	s.Expenses = append(s.Expenses, Expense{ID: "e", Name: "Упаковка", Amount: 12000, Category: "Закупки", Date: demoDate})
	d := dashboard(s, 30, 0)
	if d.Profit != b.Profit-12000 || d.Forecast[13].Balance != b.Forecast[13].Balance-12000 || d.Revenue != b.Revenue {
		t.Fatal("expense accounting mismatch")
	}
	s.Expenses[0].Date = "2026-08-01"
	d = dashboard(s, 7, 0)
	base := dashboard(initialState(), 7, 0)
	if d.Profit != base.Profit {
		t.Fatal("old expense included in current period")
	}
}
func TestHistoryPeriods(t *testing.T) {
	for _, n := range []int{7, 30, 90} {
		d := dashboard(initialState(), n, 0)
		if len(d.History) != n || d.History[n-1].Date != demoDate || d.Revenue <= 0 || d.Profit != d.Revenue-d.Expenses || d.Average != d.Revenue/d.Orders {
			t.Fatalf("bad totals for %d", n)
		}
	}
}
func TestExpenseValidation(t *testing.T) {
	valid := Expense{Name: "Расход", Amount: 10, Category: "Закупки", Date: demoDate}
	cases := []Expense{valid, valid, valid, valid, valid, valid}
	cases[0].Amount = 0
	cases[1].Amount = -1
	cases[2].Date = "2026-02-30"
	cases[3].Date = "2027-01-01"
	cases[4].Name = " "
	cases[5].Category = "unknown"
	for _, e := range cases {
		if validateExpense(e) == nil {
			t.Fatalf("accepted invalid: %+v", e)
		}
	}
	if validateExpense(valid) != nil {
		t.Fatal("valid rejected")
	}
}
func TestAPIAndPersistence(t *testing.T) {
	a := testApp(t)
	body := `{"name":"Упаковка","amount":3500,"category":"Закупки","date":"2026-10-27"}`
	if w := request(a, "POST", "/api/expenses", body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	again, err := newApp(a.file, Config{})
	if err != nil || len(again.state.Expenses) != 1 {
		t.Fatal("not persisted", err)
	}
	for i := 0; i < 2; i++ {
		if w := request(a, "POST", "/api/payments/rent/pay", "{}"); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	if w := request(a, "POST", "/api/payments/nope/pay", "{}"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(a, "POST", "/api/advice/menu/toggle", "{}"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(a.snapshot().Done) != 1 {
		t.Fatal("advice not saved")
	}
	if w := request(a, "POST", "/api/reset", "{}"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(a.snapshot().Expenses) != 0 || a.snapshot().Payments[3].Paid {
		t.Fatal("reset failed")
	}
}
func TestConcurrentWrites(t *testing.T) {
	a := testApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(a, "POST", "/api/expenses", fmt.Sprintf(`{"name":"Расход %d","amount":100,"category":"Прочее","date":"2026-10-27"}`, i))
			if w.Code != 201 {
				t.Errorf("write %d: %d", i, w.Code)
			}
		}(i)
	}
	wg.Wait()
	s := a.snapshot()
	if len(s.Expenses) != 40 {
		t.Fatalf("lost writes: %d", len(s.Expenses))
	}
	ids := map[string]bool{}
	for _, e := range s.Expenses {
		if ids[e.ID] {
			t.Fatal("duplicate id")
		}
		ids[e.ID] = true
	}
	if _, err := newApp(a.file, Config{}); err != nil {
		t.Fatal("broken persistence", err)
	}
}
func TestImportAtomicAndDeduplicated(t *testing.T) {
	a := testApp(t)
	csv := "date;name;category;amount\n2026-10-27;Стаканы;Закупки;-3500\n2026-10-27;Салфетки;Закупки;-800"
	b, _ := json.Marshal(map[string]string{"csv": csv})
	w := request(a, "POST", "/api/import", string(b))
	if w.Code != 201 || len(a.snapshot().Expenses) != 2 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(a, "POST", "/api/import", string(b))
	if w.Code != 409 || len(a.snapshot().Expenses) != 2 {
		t.Fatal("duplicate import accepted")
	}
	bad := "date;name;category;amount\n2026-10-27;Новый расход;Закупки;-300\n2026-10-27;Ошибочный;Закупки;0"
	b, _ = json.Marshal(map[string]string{"csv": bad})
	w = request(a, "POST", "/api/import", string(b))
	if w.Code != 400 || len(a.snapshot().Expenses) != 2 {
		t.Fatal("partial import")
	}
}
func TestCSVExportAndFormulaProtection(t *testing.T) {
	a := testApp(t)
	a.state.Expenses = []Expense{{ID: "1", Name: "=HYPERLINK(1)", Amount: 100, Category: "Прочее", Date: demoDate}}
	w := request(a, "GET", "/api/report.csv?period=7", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/csv") || !strings.Contains(w.Body.String(), "'=HYPERLINK(1)") || !strings.HasPrefix(w.Body.String(), "\ufeff") {
		t.Fatal("unsafe or invalid CSV", w.Body)
	}
	for _, v := range []string{" ", "", "text"} {
		if csvSafe(v) != v {
			t.Fatal(v)
		}
	}
}
func TestInputAndOriginProtection(t *testing.T) {
	a := testApp(t)
	for _, body := range []string{`{}`, `{"name":"a","amount":-1,"category":"Закупки","date":"2026-10-27"}`, `{"extra":1}`, `{} {}`} {
		if w := request(a, "POST", "/api/expenses", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/reset", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://another-site.example")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin allowed")
	}
	if w = request(a, "GET", "/api/missing", ""); w.Code != 404 {
		t.Fatal("missing API accepted")
	}
	if w = request(a, "GET", "/api/dashboard?uplift=500", ""); w.Code != 400 {
		t.Fatal("invalid scenario accepted")
	}
}
func TestStaticAndHealth(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"/", "/styles.css", "/model.js", "/app.js", "/icon.svg", "/api/health", "/api/state", "/api/dashboard?period=7"} {
		w := request(a, "GET", path, "")
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing header")
		}
	}
	if request(a, "GET", "/.env", "").Code != 404 {
		t.Fatal("secret exposed")
	}
}
func TestRulesChatAndHistoryValidation(t *testing.T) {
	a := testApp(t)
	for _, q := range []string{"Как избежать разрыва?", "Какие налоги?", "Какая прибыль?", "Привет"} {
		b, _ := json.Marshal(map[string]string{"message": q})
		w := request(a, "POST", "/api/chat", string(b))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"rules"`) {
			t.Fatal(w.Body)
		}
	}
	for _, b := range []string{`{"message":""}`, `{"message":"a","history":[{"role":"system","content":"evil"}]}`} {
		if request(a, "POST", "/api/chat", b).Code != 400 {
			t.Fatal("invalid history accepted")
		}
	}
}
func TestAIAdapterUsesContextAndKeepsKeyPrivate(t *testing.T) {
	var received struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret-key" {
			t.Error("bad provider request")
		}
		json.NewDecoder(r.Body).Decode(&received)
		send(w, 200, map[string]any{"choices": []any{map[string]any{"message": Message{Role: "assistant", Content: "В прогнозе не хватает 24 800 ₽."}}}})
	}))
	defer mock.Close()
	a := testApp(t)
	a.config = Config{BaseURL: mock.URL + "/v1", Model: "test-model", Key: "test-secret-key"}
	w := request(a, "POST", "/api/chat", `{"message":"Почему разрыв?","history":[{"role":"user","content":"привет"},{"role":"assistant","content":"здравствуйте"}],"period":30}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"ai"`) {
		t.Fatal(w.Body)
	}
	if len(received.Messages) != 4 || !strings.Contains(received.Messages[0].Content, `"gap":24800`) || received.Model != "test-model" {
		t.Fatal("context or history missing")
	}
	health := request(a, "GET", "/api/health", "").Body.String()
	if strings.Contains(health, "secret") || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("key leaked")
	}
}
func TestAIConfigurationValidatesProvider(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"choices": []any{map[string]any{"message": Message{Role: "assistant", Content: "Готово"}}}})
	}))
	defer mock.Close()
	a := testApp(t)
	b, _ := json.Marshal(map[string]string{"baseURL": mock.URL + "/v1", "model": "test-model", "key": "test-secret-key"})
	w := request(a, "POST", "/api/ai/config", string(b))
	if w.Code != 200 || a.aiConfig().Key != "test-secret-key" || strings.Contains(w.Body.String(), "test-secret-key") {
		t.Fatal("configuration failed", w.Body)
	}
	bad := request(a, "POST", "/api/ai/config", `{"baseURL":"http://untrusted.example/v1","model":"a","key":"long-secret"}`)
	if bad.Code != 400 {
		t.Fatal("insecure remote API accepted")
	}
}
func TestProviderErrorsAreHonest(t *testing.T) {
	for _, mode := range []string{"status", "empty", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "status":
					w.WriteHeader(401)
					io.WriteString(w, "sensitive-provider-detail")
				case "empty":
					io.WriteString(w, `{"choices":[]}`)
				default:
					io.WriteString(w, "not json")
				}
			}))
			defer mock.Close()
			a := testApp(t)
			a.config = Config{BaseURL: mock.URL, Key: "secret", Model: "model"}
			w := request(a, "POST", "/api/chat", `{"message":"Привет"}`)
			if w.Code != 502 || strings.Contains(w.Body.String(), "sensitive-provider-detail") || strings.Contains(w.Body.String(), `"mode":"rules"`) {
				t.Fatal("provider error hidden or leaked", w.Code, w.Body)
			}
		})
	}
}
func TestBadStateDoesNotSilentlyReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("broken"), 0600)
	if _, err := newApp(path, Config{}); err == nil {
		t.Fatal("corrupt data silently overwritten")
	}
}
