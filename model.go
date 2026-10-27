package main

import (
	"fmt"
	"strings"
	"time"
)

const demoDate = "2026-10-27"

type Expense struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Amount   int64  `json:"amount"`
	Date     string `json:"date"`
}
type Payment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Amount   int64  `json:"amount"`
	Day      int    `json:"day"`
	Paid     bool   `json:"paid"`
}
type State struct {
	Version     int          `json:"version"`
	Balance     int64        `json:"balance"`
	Expenses    []Expense    `json:"expenses"`
	Payments    []Payment    `json:"payments"`
	Done        []string     `json:"done"`
	Tax         TaxConfig    `json:"tax"`
	RevenueDays []RevenueDay `json:"revenueDays"`
	Plan        *CashPlan    `json:"plan"`
}
type Day struct {
	Date     string `json:"date"`
	Revenue  int64  `json:"revenue"`
	Refunds  int64  `json:"refunds"`
	Expenses int64  `json:"expenses"`
	Orders   int64  `json:"orders"`
}
type Point struct {
	Date    string `json:"date"`
	Balance int64  `json:"balance"`
	Income  int64  `json:"income"`
	Outflow int64  `json:"outflow"`
}
type Dashboard struct {
	Period             int     `json:"period"`
	Revenue            int64   `json:"revenue"`
	Profit             int64   `json:"profit"`
	Expenses           int64   `json:"expenses"`
	Refunds            int64   `json:"refunds"`
	Orders             int64   `json:"orders"`
	Average            int64   `json:"average"`
	Margin             float64 `json:"margin"`
	Score              float64 `json:"score"`
	LiquidityScore     float64 `json:"liquidityScore"`
	ProfitabilityScore float64 `json:"profitabilityScore"`
	MinBalance         int64   `json:"minBalance"`
	Gap                int64   `json:"gap"`
	GapDate            string  `json:"gapDate"`
	Forecast           []Point `json:"forecast"`
	History            []Day   `json:"history"`
}

func initialState() State {
	return State{Version: 2, Balance: 220400, Tax: defaultTax(), Expenses: []Expense{}, Done: []string{}, Payments: []Payment{
		{ID: "supply", Name: "Поставка зерна и молока", Category: "Закупки", Amount: 45000, Day: 2},
		{ID: "salary", Name: "Зарплата команды", Category: "Команда", Amount: 70000, Day: 5},
		{ID: "tax", Name: "Аванс УСН за 9 месяцев", Category: "Налоги", Amount: 66000, Day: 1},
		{ID: "rent", Name: "Аренда кофейни", Category: "Аренда", Amount: 108000, Day: 12},
	}}
}

func paymentContext(payments []Payment) []map[string]any {
	base, _ := time.Parse("2006-01-02", demoDate)
	result := make([]map[string]any, 0, len(payments))
	for _, p := range payments {
		result = append(result, map[string]any{"name": p.Name, "category": p.Category, "amount": p.Amount, "date": base.AddDate(0, 0, p.Day).Format("2006-01-02"), "daysFromDemoDate": p.Day, "paid": p.Paid})
	}
	return result
}
func history(days int) []Day {
	base, _ := time.Parse("2006-01-02", demoDate)
	result := make([]Day, 0, days)
	for i := 90 - days; i < 90; i++ {
		orders := int64(34 + (i*17)%19)
		if i%7 == 5 || i%7 == 6 {
			orders += 12
		}
		avg := int64(445 + (i*7)%38)
		if i > 75 {
			avg -= int64((i - 75) * 5)
		}
		refunds := int64(0)
		if i%9 == 0 {
			refunds = avg
		}
		revenue := orders * avg
		result = append(result, Day{Date: base.AddDate(0, 0, i-89).Format("2006-01-02"), Revenue: revenue, Refunds: refunds, Expenses: revenue*57/100 + 1500, Orders: orders})
	}
	return result
}
func forecast(s State, uplift int) []Point {
	base, _ := time.Parse("2006-01-02", demoDate)
	balance := availableBalance(s)
	result := make([]Point, 0, 14)
	for i := 1; i <= 14; i++ {
		income := int64(12300 + (i*1100)%4500)
		dailyExpenses := int64(11300)
		if s.Plan != nil {
			income = s.Plan.DailyRevenue
			dailyExpenses = s.Plan.DailyExpenses
		}
		income = income * int64(100+uplift) / 100
		out := dailyExpenses
		for _, p := range s.Payments {
			if !p.Paid && p.Day == i {
				out += p.Amount
			}
		}
		balance += income - out
		result = append(result, Point{Date: base.AddDate(0, 0, i).Format("2006-01-02"), Balance: balance, Income: income, Outflow: out})
	}
	return result
}
func dashboard(s State, period, uplift int) Dashboard {
	d := Dashboard{Period: period, History: adjustedHistory(s, period), Forecast: forecast(s, uplift)}
	for _, h := range d.History {
		d.Revenue += h.Revenue - h.Refunds
		d.Refunds += h.Refunds
		d.Expenses += h.Expenses
		d.Orders += h.Orders
	}
	start := d.History[0].Date
	for _, e := range s.Expenses {
		if e.Date >= start && e.Date <= demoDate {
			d.Expenses += e.Amount
		}
	}
	d.Profit = d.Revenue - d.Expenses
	if d.Orders > 0 {
		d.Average = d.Revenue / d.Orders
	}
	if d.Revenue > 0 {
		d.Margin = float64(d.Profit) * 100 / float64(d.Revenue)
	}
	d.MinBalance = availableBalance(s)
	if d.MinBalance < 0 {
		d.Gap = -d.MinBalance
		d.GapDate = demoDate
	}
	for _, p := range d.Forecast {
		d.MinBalance = min(d.MinBalance, p.Balance)
		if p.Balance < 0 && d.GapDate == "" {
			d.GapDate = p.Date
		}
		if -p.Balance > d.Gap {
			d.Gap = -p.Balance
		}
	}
	dailyExpenses := int64(11300)
	if s.Plan != nil {
		dailyExpenses = s.Plan.DailyExpenses
	}
	d.Score, d.LiquidityScore, d.ProfitabilityScore = healthScores(d.MinBalance, d.Forecast[len(d.Forecast)-1].Balance, d.Margin, dailyExpenses)
	return d
}
func validateExpense(e Expense) error {
	if strings.ContainsAny(e.Name, "<>\r\n\t") {
		return fmt.Errorf("используйте обычный текст без HTML и переносов строк")
	}
	if strings.TrimSpace(e.Name) == "" || len([]rune(e.Name)) > 100 {
		return fmt.Errorf("название должно содержать от 1 до 100 символов")
	}
	if e.Amount <= 0 || e.Amount > 100000000 {
		return fmt.Errorf("сумма должна быть от 1 до 100 000 000 рублей")
	}
	valid := map[string]bool{"Закупки": true, "Команда": true, "Аренда": true, "Налоги": true, "Маркетинг": true, "Прочее": true}
	if !valid[e.Category] {
		return fmt.Errorf("неизвестная категория")
	}
	date, err := time.Parse("2006-01-02", e.Date)
	base, _ := time.Parse("2006-01-02", demoDate)
	if err != nil || date.After(base) || date.Before(base.AddDate(-1, 0, 0)) {
		return fmt.Errorf("укажите прошедшую дату в пределах года до 27 октября 2026")
	}
	return nil
}
func localAnswer(question string, d Dashboard, s State) string {
	q := strings.ToLower(question)
	switch {
	case strings.Contains(q, "налог") || strings.Contains(q, "срок"):
		c := estimateTax(s.Tax)
		return fmt.Sprintf("Выбран ИП на УСН Доходы 6%% без НДС. По данным за 9 месяцев начислено %d ₽, вычет взносов — %d ₽, предыдущие авансы — %d ₽. К уплате %d ₽ до 28 октября 2026. При работниках вычет ограничен 50%% налога. Проверьте базу и вычеты в налоговом плане. Региональные льготы не учтены.", c.Gross, c.Deduction, s.Tax.PriorAdvances, c.Payable)
	case strings.Contains(q, "прибыл") || strings.Contains(q, "расход") || strings.Contains(q, "чек"):
		return fmt.Sprintf("За выбранные %d дней выручка за вычетом возвратов — %d ₽, учтённые операционные расходы — %d ₽, операционная прибыль — %d ₽, маржа — %.1f%%. Средний чек за период — %d ₽. Проверьте закупочные цены и списания, прежде чем менять цены в меню. Прибыль здесь не равна остатку на счёте и не включает расчёт налога.", d.Period, d.Revenue, d.Expenses, d.Profit, d.Margin, d.Average)
	case strings.Contains(q, "разрыв") || strings.Contains(q, "прогноз") || strings.Contains(q, "план") || strings.Contains(q, "делать"):
		if d.Gap > 0 {
			return fmt.Sprintf("В выбранном сценарии первый отрицательный остаток — %s, максимальная нехватка за 14 дней — %d ₽. В прогноз входят текущий остаток, ожидаемые поступления, ежедневные расходы и обязательства. План: 1) согласовать график закупок; 2) проверить возможность переноса части аренды без штрафов; 3) сравнить сценарий роста выручки на 15%%. Это гипотезы для проверки, а не обещание результата. Откройте переключатель сценария на графике.", d.GapDate, d.Gap)
		}
		return "В выбранном сценарии отрицательного остатка на 14 дней нет. Сохраняйте резерв под обязательства и пересматривайте прогноз после новых расходов. Это сценарная оценка, а не гарантия."
	default:
		return fmt.Sprintf("Остаток кофейни — %d ₽, индекс здоровья бизнеса — %.1f из 100. Могу разобрать расходы, объяснить прогноз или составить план действий. Для свободного диалога подключите модель в настройках ИИ.", availableBalance(s), d.Score)
	}
}
