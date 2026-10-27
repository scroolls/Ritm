package main

import (
	"math"
	"net/http"
	"time"
)

// RevenueDay replaces the day's net revenue, rather than adding it twice.
type RevenueDay struct {
	Date   string `json:"date"`
	Amount int64  `json:"amount"`
	Orders int64  `json:"orders"`
}
type CashPlan struct {
	DailyRevenue  int64 `json:"dailyRevenue"`
	DailyExpenses int64 `json:"dailyExpenses"`
}

func adjustedHistory(s State, period int) []Day {
	rows := history(period)
	for i := range rows {
		for _, v := range s.RevenueDays {
			if rows[i].Date == v.Date {
				rows[i].Revenue, rows[i].Refunds, rows[i].Orders = v.Amount, 0, v.Orders
			}
		}
	}
	return rows
}
func revenueAdjustment(s State) int64 {
	var delta int64
	for _, h := range history(90) {
		for _, v := range s.RevenueDays {
			if v.Date == h.Date {
				delta += v.Amount - (h.Revenue - h.Refunds)
			}
		}
	}
	return delta
}
func availableBalance(s State) int64 {
	n := s.Balance + revenueAdjustment(s)
	for _, e := range s.Expenses {
		n -= e.Amount
	}
	for _, p := range s.Payments {
		if p.Paid {
			n -= p.Amount
		}
	}
	return n
}
func healthScores(minBalance, closingBalance int64, margin float64, dailyExpenses int64) (float64, float64, float64) {
	// Smooth liquidity scoring has no flat zero-reserve band. Both downside and recovery matter.
	reserve := float64(minBalance)*0.7 + float64(closingBalance)*0.3
	liquidity := 35 + 35*reserve/(math.Abs(reserve)+float64(max(1, dailyExpenses)*3))
	profitability := math.Max(0, math.Min(30, margin))
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	return round(liquidity + profitability), round(liquidity), round(profitability)
}
func (a *App) updateRevenue(w http.ResponseWriter, r *http.Request) {
	var v RevenueDay
	if !decode(w, r, &v) {
		return
	}
	date, err := time.Parse("2006-01-02", v.Date)
	base, _ := time.Parse("2006-01-02", demoDate)
	if err != nil || date.After(base) || date.Before(base.AddDate(0, 0, -89)) || v.Amount < 0 || v.Amount > 100000000 || v.Orders < 0 || v.Orders > 1000000 || (v.Amount > 0 && v.Orders == 0) || (v.Amount == 0 && v.Orders != 0) {
		fail(w, 400, "Проверьте дату, выручку и число покупок. При нулевой выручке укажите ноль покупок.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.RevenueDays = append([]RevenueDay{}, s.RevenueDays...)
	found := false
	for i, day := range s.RevenueDays {
		if day.Date == v.Date {
			s.RevenueDays[i] = v
			found = true
			break
		}
	}
	if !found {
		s.RevenueDays = append(s.RevenueDays, v)
	}
	if a.persist(s) != nil {
		fail(w, 500, "Не удалось сохранить выручку")
		return
	}
	send(w, 200, dashboard(s, 30, 0))
}
func (a *App) updateCashPlan(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Balance       *int64 `json:"balance"`
		DailyRevenue  *int64 `json:"dailyRevenue"`
		DailyExpenses *int64 `json:"dailyExpenses"`
	}
	if !decode(w, r, &v) {
		return
	}
	if v.Balance == nil || v.DailyRevenue == nil || v.DailyExpenses == nil {
		fail(w, 400, "Заполните остаток, поступления и расходы.")
		return
	}
	if *v.Balance < -100000000 || *v.Balance > 100000000 || *v.DailyRevenue < 0 || *v.DailyRevenue > 100000000 || *v.DailyExpenses < 0 || *v.DailyExpenses > 100000000 {
		fail(w, 400, "Укажите целые суммы в пределах 100 млн ₽.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	// A user enters the current balance. Back out already recorded adjustments.
	s.Balance += *v.Balance - availableBalance(s)
	s.Plan = &CashPlan{DailyRevenue: *v.DailyRevenue, DailyExpenses: *v.DailyExpenses}
	if a.persist(s) != nil {
		fail(w, 500, "Не удалось сохранить финансовый план")
		return
	}
	send(w, 200, dashboard(s, 30, 0))
}

func (a *App) updatePayment(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Amount int64 `json:"amount"`
		Day    int   `json:"day"`
	}
	if !decode(w, r, &v) {
		return
	}
	if v.Amount <= 0 || v.Amount > 100000000 || v.Day < 1 || v.Day > 90 {
		fail(w, 400, "Укажите положительную сумму и дату в пределах следующих 90 дней.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.Payments = append([]Payment{}, s.Payments...)
	for i, p := range s.Payments {
		if p.ID != r.PathValue("id") {
			continue
		}
		if p.Paid {
			fail(w, 409, "Оплаченный платёж нельзя изменить.")
			return
		}
		if p.ID == "tax" {
			fail(w, 409, "Измените налог через налоговый расчёт.")
			return
		}
		s.Payments[i].Amount = v.Amount
		s.Payments[i].Day = v.Day
		if a.persist(s) != nil {
			fail(w, 500, "Не удалось сохранить платёж")
			return
		}
		send(w, 200, dashboard(s, 30, 0))
		return
	}
	fail(w, 404, "Платёж не найден")
}
