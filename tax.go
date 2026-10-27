package main

import "net/http"

type TaxConfig struct {
	Income        int64 `json:"income"`
	Contributions int64 `json:"contributions"`
	PriorAdvances int64 `json:"priorAdvances"`
	Employees     bool  `json:"employees"`
}
type TaxEstimate struct {
	Gross     int64  `json:"gross"`
	Deduction int64  `json:"deduction"`
	Payable   int64  `json:"payable"`
	Due       string `json:"due"`
}

func defaultTax() TaxConfig {
	return TaxConfig{Income: 4800000, Contributions: 90000, PriorAdvances: 132000, Employees: true}
}
func estimateTax(c TaxConfig) TaxEstimate {
	gross := (c.Income*6 + 50) / 100
	cap := gross
	if c.Employees {
		cap = gross / 2
	}
	deduction := min(c.Contributions, cap)
	return TaxEstimate{Gross: gross, Deduction: deduction, Payable: max(0, gross-deduction-c.PriorAdvances), Due: "2026-10-28"}
}
func (a *App) updateTax(w http.ResponseWriter, r *http.Request) {
	var c TaxConfig
	if !decode(w, r, &c) {
		return
	}
	if c.Income < 0 || c.Income > 20000000 || c.Contributions < 0 || c.Contributions > 10000000 || c.PriorAdvances < 0 || c.PriorAdvances > 10000000 {
		fail(w, 400, "Проверьте суммы. Калькулятор рассчитан на УСН Доходы 6% без НДС при доходе до 20 млн ₽.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.Payments = append([]Payment{}, s.Payments...)
	for i, p := range s.Payments {
		if p.ID == "tax" {
			if p.Paid {
				fail(w, 409, "Аванс уже отмечен оплаченным. Для изменения налогового сценария сначала восстановите исходные данные.")
				return
			}
			s.Payments[i].Amount = estimateTax(c).Payable
		}
	}
	s.Tax = c
	if a.persist(s) != nil {
		fail(w, 500, "Не удалось сохранить расчёт")
		return
	}
	send(w, 200, map[string]any{"config": c, "estimate": estimateTax(c)})
}
func (a *App) deleteExpense(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	found := false
	next := make([]Expense, 0, len(s.Expenses))
	for _, e := range s.Expenses {
		if e.ID == r.PathValue("id") {
			found = true
		} else {
			next = append(next, e)
		}
	}
	if !found {
		fail(w, 404, "Расход не найден")
		return
	}
	s.Expenses = next
	if a.persist(s) != nil {
		fail(w, 500, "Не удалось удалить расход")
		return
	}
	send(w, 200, map[string]bool{"deleted": true})
}
