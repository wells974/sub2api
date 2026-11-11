package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

const AccountOpsPoolKind = "account_pool"

// This extension uses AccountOps' channels and delivery queue, independently of
// either credential guard's lifecycle. Nil configuration is opt-out.
type AccountOpsPoolConfig struct {
	Enabled      bool `json:"enabled"`
	NotifyStates bool `json:"notify_states"`
	NotifyCounts bool `json:"notify_counts"`
}

func (c *AccountOpsPoolConfig) clone() *AccountOpsPoolConfig {
	if c == nil {
		return nil
	}
	copy := *c
	return &copy
}

func (c AccountOpsConfig) validatePoolNotifications() error {
	if c.PoolNotifications != nil && c.PoolNotifications.Enabled && !c.PoolEnabled() {
		return accountOpsConfigValidation("select at least one pool change type")
	}
	return nil
}

func (c AccountOpsConfig) PoolEnabled() bool {
	return c.PoolNotifications != nil && c.PoolNotifications.Enabled &&
		(c.PoolNotifications.NotifyStates || c.PoolNotifications.NotifyCounts)
}

type AccountOpsPoolAccount struct {
	Account
	ProbeState    string
	FailStreak    int
	ReloginFailed bool
}

type accountOpsPoolState struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason"`
	ReloginFailed bool   `json:"relogin_failed"`
}

type accountOpsPoolSnapshot struct {
	Accounts  map[int64]accountOpsPoolState `json:"accounts"`
	Available int                           `json:"available"`
	Total     int                           `json:"total"`
}

type AccountOpsPoolChange struct {
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	Before      string `json:"before,omitempty"`
	After       string `json:"after"`
}

type AccountOpsPoolDetails struct {
	Available         int                    `json:"available"`
	Total             int                    `json:"total"`
	PreviousAvailable *int                   `json:"previous_available,omitempty"`
	PreviousTotal     *int                   `json:"previous_total,omitempty"`
	Changes           []AccountOpsPoolChange `json:"changes"`
	Omitted           int                    `json:"omitted,omitempty"`
}

// The repository invokes this pure observer while holding the durable baseline
// lock, then commits the next baseline and immutable event together. No network
// calls or notification delivery may happen inside this callback.
type AccountOpsPoolObserver func(string, []AccountOpsPoolAccount, AccountOpsPoolConfig) (string, *AccountOpsEvent, error)
type accountOpsPoolRepository interface {
	ObserveAccountOpsPool(context.Context, AccountOpsPoolObserver) error
}

func (s *AccountOpsService) scanAccountPool(ctx context.Context) {
	c := s.currentConfig()
	repo, ok := s.repo.(accountOpsPoolRepository)
	if !ok || !c.Allows(AccountOpsPoolKind) {
		return
	}
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := repo.ObserveAccountOpsPool(query, ObserveAccountOpsPool); err != nil {
		s.failures.Add(1)
	}
}

func poolAccountState(a AccountOpsPoolAccount) accountOpsPoolState {
	state := accountOpsPoolState{Name: poolSafeName(a.Name), Available: a.IsSchedulable(), ReloginFailed: a.ReloginFailed}
	switch {
	case a.ProbeState == "auth" || a.FailStreak > 0:
		state.Available, state.Reason = false, "凭证失效"
	case a.Status == StatusError:
		state.Reason = "账号异常"
	case !state.Available:
		state.Reason = "不可调度"
	default:
		state.Reason = "可用"
	}
	return state
}

func poolSafeName(name string) string {
	runes := []rune(strings.Join(strings.Fields(name), " "))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

func poolStateLabel(state accountOpsPoolState) string {
	if state.ReloginFailed {
		return state.Reason + "；重登失败"
	}
	return state.Reason
}

// ObserveAccountOpsPool emits at most one bounded digest per scan, while keeping
// every account in the baseline. An unknown/transient probe never clears a
// persisted authentication failure. Renames alone do not create notifications.
func ObserveAccountOpsPool(previous string, accounts []AccountOpsPoolAccount, config AccountOpsPoolConfig) (string, *AccountOpsEvent, error) {
	var old accountOpsPoolSnapshot
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), &old); err != nil {
			return "", nil, err
		}
	}
	next := accountOpsPoolSnapshot{Accounts: make(map[int64]accountOpsPoolState)}
	for _, a := range accounts {
		state := poolAccountState(a)
		next.Accounts[a.ID] = state
		if state.Available {
			next.Available++
		}
		next.Total++
	}
	details := &AccountOpsPoolDetails{Available: next.Available, Total: next.Total, Changes: []AccountOpsPoolChange{}}
	if previous != "" {
		details.PreviousAvailable, details.PreviousTotal = &old.Available, &old.Total
	}
	ids := make([]int64, 0, len(next.Accounts)+len(old.Accounts))
	for id := range next.Accounts {
		ids = append(ids, id)
	}
	for id := range old.Accounts {
		if _, exists := next.Accounts[id]; !exists {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	onlyRecovery := previous != ""
	if config.NotifyStates {
		for _, id := range ids {
			before, existed := old.Accounts[id]
			after, exists := next.Accounts[id]
			if existed && exists && before.Available == after.Available && before.Reason == after.Reason && before.ReloginFailed == after.ReloginFailed {
				continue
			}
			if previous == "" && after.Available && !after.ReloginFailed {
				continue
			}
			change := AccountOpsPoolChange{AccountID: id, AccountName: after.Name, After: poolStateLabel(after)}
			if existed {
				change.Before = poolStateLabel(before)
			}
			if !exists {
				change.AccountName, change.After = before.Name, "已移出号池"
			}
			if !exists || !after.Available || after.ReloginFailed {
				onlyRecovery = false
			}
			if len(details.Changes) < 10 {
				details.Changes = append(details.Changes, change)
			} else {
				details.Omitted++
			}
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return "", nil, err
	}
	changedCounts := previous == "" || old.Available != next.Available || old.Total != next.Total
	if !config.Enabled || (len(details.Changes) == 0 && !(config.NotifyCounts && changedCounts)) {
		return string(raw), nil, nil
	}
	phase := "alert"
	if onlyRecovery && next.Available >= old.Available && next.Total == old.Total {
		phase = "recovery"
	}
	return string(raw), &AccountOpsEvent{Kind: AccountOpsPoolKind, GroupID: "pool:all", GroupName: "全部号池", AccountName: "全部号池", Phase: phase, Signal: "pool_state_change", Details: &AccountOpsDetails{Pool: details}}, nil
}

func (s *AccountOpsService) poolNotificationMessage(e *AccountOpsEvent) accountOpsMessage {
	title := "号池状态变更"
	if e.Phase == "recovery" {
		title = "号池状态恢复"
	}
	lines := []string{}
	if e.Details != nil && e.Details.Pool != nil {
		d := e.Details.Pool
		lines = append(lines, fmt.Sprintf("可用 / 总数：%d / %d", d.Available, d.Total))
		if d.PreviousAvailable != nil && d.PreviousTotal != nil {
			lines = append(lines, fmt.Sprintf("变更前：%d / %d", *d.PreviousAvailable, *d.PreviousTotal))
		}
		for _, change := range d.Changes {
			state := change.After
			if change.Before != "" {
				state = change.Before + " → " + state
			}
			lines = append(lines, fmt.Sprintf("%s (#%d)：%s", change.AccountName, change.AccountID, state))
		}
		if d.Omitted > 0 {
			lines = append(lines, fmt.Sprintf("另有 %d 个账号状态变化，请查看账号列表。", d.Omitted))
		}
	}
	observed := e.LastSeen.In(s.timezone).Format("2006-01-02 15:04:05 MST")
	lines = append(lines, "检测时间："+observed)
	plain := strings.Join(lines, "\n")
	markdown := "### " + title
	for _, line := range lines {
		markdown += "\n" + escapeOpsMarkdown(line)
	}
	color := "orange"
	if e.Phase == "recovery" {
		color = "green"
	}
	return accountOpsMessage{title: title, robotTitle: title, plain: plain, markdown: markdown,
		html:  "<h2>" + title + "</h2><p>" + strings.ReplaceAll(html.EscapeString(plain), "\n", "<br>") + "</p>",
		color: color, account: "全部号池", observedTime: observed}
}
