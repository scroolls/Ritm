package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHealthRespondsToFinancialChanges(t *testing.T) {
	s := initialState()
	base := dashboard(s, 30, 0)
	s.Expenses = append(s.Expenses, Expense{ID: "e", Name: "Закупка", Category: "Закупки", Amount: 5000, Date: demoDate})
	lower := dashboard(s, 30, 0)
	if lower.Score >= base.Score {
		t.Fatal("expense did not lower index", base.Score, lower.Score)
	}
	s.Expenses = nil
	s.Balance += 5000
	if dashboard(s, 30, 0).Score <= base.Score {
		t.Fatal("cash reserve did not improve index")
	}
	for _, margin := range []float64{-20, 0, 15, 30, 70} {
		for _, cash := range []int64{-1000000, 0, 1000000} {
			score, _, _ := healthScores(cash, cash+3000, margin, 11300)
			if score < 0 || score > 100 {
				t.Fatal("index outside bounds")
			}
		}
	}
}
func TestRevenueReplacementPersistenceAndZeroDay(t *testing.T) {
	a := testApp(t)
	base := dashboard(a.snapshot(), 30, 0)
	day := base.History[29]
	amount := day.Revenue - day.Refunds + 10000
	body := fmt.Sprintf(`{"date":"%s","amount":%d,"orders":60}`, demoDate, amount)
	for i := 0; i < 2; i++ {
		if w := request(a, "PUT", "/api/revenue", body); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	d := dashboard(a.snapshot(), 30, 0)
	if d.Revenue != base.Revenue+10000 || d.Profit != base.Profit+10000 || d.Forecast[13].Balance != base.Forecast[13].Balance+10000 || d.Score <= base.Score || len(a.snapshot().RevenueDays) != 1 {
		t.Fatal("revenue duplicated or not propagated")
	}
	again, err := newApp(a.file, Config{})
	if err != nil || dashboard(again.snapshot(), 30, 0).Revenue != d.Revenue {
		t.Fatal("revenue not persisted", err)
	}
	if w := request(a, "PUT", "/api/revenue", `{"date":"2026-10-27","amount":0,"orders":0}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	last := dashboard(a.snapshot(), 30, 0).History[29]
	if last.Revenue != 0 || last.Orders != 0 {
		t.Fatal("closed day rejected")
	}
}
func TestFinancePlanPreservesActualAccounting(t *testing.T) {
	a := testApp(t)
	request(a, "POST", "/api/payments/rent/pay", "{}")
	request(a, "POST", "/api/expenses", `{"name":"Молоко","amount":3500,"category":"Закупки","date":"2026-10-27"}`)
	body := `{"balance":160000,"dailyRevenue":21000,"dailyExpenses":10000}`
	for i := 0; i < 2; i++ {
		if w := request(a, "PUT", "/api/finance-plan", body); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	s := a.snapshot()
	if availableBalance(s) != 160000 || !s.Payments[3].Paid || len(s.Expenses) != 1 {
		t.Fatal("current balance drift or lost transactions")
	}
	d := dashboard(s, 30, 0)
	if d.Forecast[0].Income != 21000 || d.Forecast[0].Outflow != 76000 {
		t.Fatal("forecast ignored user plan", d.Forecast[0])
	}
	a2, err := newApp(a.file, Config{})
	if err != nil || a2.state.Plan.DailyRevenue != 21000 {
		t.Fatal("plan not persisted")
	}
}
func TestFinanceInputsRejectInvalidValues(t *testing.T) {
	a := testApp(t)
	for _, body := range []string{`{"date":"2026-10-28","amount":1,"orders":1}`, `{"date":"2026-02-30","amount":1,"orders":1}`, `{"date":"2026-10-27","amount":-1,"orders":1}`, `{"date":"2026-10-27","amount":1,"orders":0}`, `{"date":"2026-10-27","amount":0,"orders":1}`, `{"date":"2026-10-27","amount":1.5,"orders":1}`, `{"date":"2026-10-27","amount":100000001,"orders":1}`} {
		if request(a, "PUT", "/api/revenue", body).Code != 400 {
			t.Fatal("accepted invalid revenue", body)
		}
	}
	for _, body := range []string{`{"balance":0,"dailyRevenue":-1,"dailyExpenses":1}`, `{"balance":100000001,"dailyRevenue":1,"dailyExpenses":1}`, `{"balance":0,"dailyRevenue":1,"dailyExpenses":-1}`} {
		if request(a, "PUT", "/api/finance-plan", body).Code != 400 {
			t.Fatal("accepted invalid plan", body)
		}
	}
}
func TestRevenueExportUsesCorrectedValues(t *testing.T) {
	a := testApp(t)
	w := request(a, "PUT", "/api/revenue", `{"date":"2026-10-27","amount":35000,"orders":70}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = request(a, "GET", "/api/dashboard", "")
	var d Dashboard
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.History[29].Revenue != 35000 || d.History[29].Orders != 70 {
		t.Fatal("dashboard ignored correction")
	}
	csv := request(a, "GET", "/api/report.csv", "").Body.String()
	if !strings.Contains(csv, "2026-10-27;Выручка минус возвраты;Выручка;35000") {
		t.Fatal("export ignored corrected revenue")
	}
}
func TestPaymentChangesRecalculateForecastWithoutMovingCash(t *testing.T) {
	a := testApp(t)
	before := dashboard(a.snapshot(), 30, 0)
	cash := availableBalance(a.snapshot())
	if w := request(a, "PUT", "/api/payments/rent", `{"amount":108000,"day":20}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	d := dashboard(a.snapshot(), 30, 0)
	if d.Gap != 0 || d.Score <= before.Score || availableBalance(a.snapshot()) != cash || a.snapshot().Payments[3].Day != 20 {
		t.Fatal("rescheduling did not affect forecast correctly")
	}
	if w := request(a, "PUT", "/api/payments/supply", `{"amount":30000,"day":2}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if dashboard(a.snapshot(), 30, 0).Forecast[13].Balance != d.Forecast[13].Balance+15000 {
		t.Fatal("edited amount ignored")
	}
	request(a, "POST", "/api/payments/salary/pay", "{}")
	if request(a, "PUT", "/api/payments/salary", `{"amount":1,"day":2}`).Code != 409 {
		t.Fatal("paid obligation changed")
	}
	if request(a, "PUT", "/api/payments/tax", `{"amount":1,"day":2}`).Code != 409 {
		t.Fatal("tax bypassed calculator")
	}
	if request(a, "PUT", "/api/payments/nope", `{"amount":1,"day":2}`).Code != 404 {
		t.Fatal("missing payment changed")
	}
}
func TestFinancialEditsRejectMissingAndInvalidFields(t *testing.T) {
	a := testApp(t)
	for _, body := range []string{`{}`, `{"balance":1}`, `{"balance":null,"dailyRevenue":1,"dailyExpenses":1}`} {
		if request(a, "PUT", "/api/finance-plan", body).Code != 400 {
			t.Fatal("missing plan field accepted")
		}
	}
	for _, body := range []string{`{"amount":0,"day":2}`, `{"amount":1,"day":0}`, `{"amount":1,"day":91}`, `{"amount":1.2,"day":2}`} {
		if request(a, "PUT", "/api/payments/rent", body).Code != 400 {
			t.Fatal("invalid payment accepted")
		}
	}
}

func TestNegativeCurrentBalanceIsAlreadyACashGap(t *testing.T) {
	s := initialState()
	s.Balance = -50000
	s.Payments = nil
	d := dashboard(s, 30, 0)
	if d.Gap != 50000 || d.GapDate != demoDate {
		t.Fatal("current cash shortfall deferred to tomorrow", d.Gap, d.GapDate)
	}
}
