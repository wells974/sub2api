package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func poolFixture() []AccountOpsPoolAccount {
	return []AccountOpsPoolAccount{
		{Account: Account{ID: 1, Name: "first", Status: StatusActive, Schedulable: true}},
		{Account: Account{ID: 2, Name: "disabled", Status: StatusDisabled}},
	}
}

func TestAccountOpsPoolTransitions(t *testing.T) {
	cfg := AccountOpsPoolConfig{Enabled: true, NotifyStates: true, NotifyCounts: true}
	accounts := poolFixture()
	previous, initial, err := ObserveAccountOpsPool("", accounts, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, initial.Details.Pool.Available)
	require.Equal(t, 2, initial.Details.Pool.Total)
	require.Nil(t, initial.Details.Pool.PreviousTotal)
	observe := func(wantEvent bool, available int, phase, label string) {
		t.Helper()
		next, event, err := ObserveAccountOpsPool(previous, accounts, cfg)
		require.NoError(t, err)
		previous = next
		if !wantEvent {
			require.Nil(t, event)
			return
		}
		require.NotNil(t, event)
		require.Equal(t, available, event.Details.Pool.Available)
		require.Equal(t, phase, event.Phase)
		if label != "" {
			require.Contains(t, event.Details.Pool.Changes[0].After, label)
		}
	}
	observe(false, 0, "", "")
	accounts[0].Name = "renamed"
	observe(false, 0, "", "")
	accounts[0].ProbeState, accounts[0].FailStreak = "auth", 1
	observe(true, 0, "alert", "凭证失效")
	accounts[0].ProbeState = "transient"
	observe(false, 0, "", "") // Network errors cannot invent a recovery.
	accounts[0].ReloginFailed = true
	observe(true, 0, "alert", "重登失败")
	observe(false, 0, "", "")
	accounts[0].ReloginFailed = false
	observe(true, 0, "alert", "凭证失效") // Repair succeeded; still awaiting a healthy probe.
	accounts[0].ProbeState, accounts[0].FailStreak = "ok", 0
	observe(true, 1, "recovery", "可用")
	accounts[0].Status = StatusError
	observe(true, 0, "alert", "账号异常")
	accounts[0].Status = StatusActive
	accounts[0].PerformancePaused = true
	observe(true, 0, "alert", "不可调度")
	accounts[0].PerformancePaused = false
	observe(true, 1, "recovery", "可用")
	accounts = accounts[:1]
	observe(true, 1, "alert", "已移出号池")
}

func TestAccountOpsPoolCountOnlyAndStateOnly(t *testing.T) {
	accounts := poolFixture()
	cfg := AccountOpsPoolConfig{Enabled: true, NotifyCounts: true}
	previous, _, err := ObserveAccountOpsPool("", accounts, cfg)
	require.NoError(t, err)
	accounts[0].ReloginFailed = true
	next, event, err := ObserveAccountOpsPool(previous, accounts, cfg)
	require.NoError(t, err)
	require.Nil(t, event, "same count, state notifications off")
	accounts[0].Schedulable = false
	previous, event, err = ObserveAccountOpsPool(next, accounts, cfg)
	require.NoError(t, err)
	require.Equal(t, "alert", event.Phase)
	require.Empty(t, event.Details.Pool.Changes)
	require.Equal(t, 0, event.Details.Pool.Available)
	require.Equal(t, 1, *event.Details.Pool.PreviousAvailable)
	cfg.NotifyCounts, cfg.NotifyStates = false, true
	accounts[0].ReloginFailed = false
	_, event, err = ObserveAccountOpsPool(previous, accounts, cfg)
	require.NoError(t, err)
	require.NotNil(t, event, "state changes must notify even when the count stays zero")
	cfg.Enabled = false
	_, event, err = ObserveAccountOpsPool("", accounts, cfg)
	require.NoError(t, err)
	require.Nil(t, event)
	_, _, err = ObserveAccountOpsPool("broken snapshot", accounts, cfg)
	require.Error(t, err, "never replace a corrupt baseline with invented transitions")
}

func TestAccountOpsPoolAvailability(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	for _, test := range []struct {
		name   string
		change func(*AccountOpsPoolAccount)
	}{
		{"disabled", func(a *AccountOpsPoolAccount) { a.Status = StatusDisabled }},
		{"scheduling off", func(a *AccountOpsPoolAccount) { a.Schedulable = false }},
		{"performance paused", func(a *AccountOpsPoolAccount) { a.PerformancePaused = true }},
		{"expired", func(a *AccountOpsPoolAccount) { a.ExpiresAt = &past; a.AutoPauseOnExpired = true }},
		{"rate limited", func(a *AccountOpsPoolAccount) { a.RateLimitResetAt = &future }},
		{"overloaded", func(a *AccountOpsPoolAccount) { a.OverloadUntil = &future }},
		{"temporary pause", func(a *AccountOpsPoolAccount) { a.TempUnschedulableUntil = &future }},
		{"auth", func(a *AccountOpsPoolAccount) { a.ProbeState = "auth" }},
		{"auth then transient", func(a *AccountOpsPoolAccount) { a.ProbeState = "transient"; a.FailStreak = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := poolFixture()[0]
			test.change(&a)
			require.False(t, poolAccountState(a).Available)
		})
	}
}

func TestAccountOpsPoolConfigPreservesOtherSettingsAndOlderClients(t *testing.T) {
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	cfg, err := svc.SaveNotificationSettings(context.Background(), AccountOpsNotificationSettings{
		PoolNotifications: &AccountOpsPoolConfig{Enabled: true, NotifyStates: true, NotifyCounts: true},
	})
	require.NoError(t, err)
	require.True(t, cfg.BalanceLow)
	require.True(t, cfg.WeeklyQuota)
	cfg.PoolNotifications = nil // An older full-config client omits this field.
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	cfg, err = svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.PoolEnabled())
	cfg.BalanceLow, cfg.WeeklyQuota, cfg.Enabled, cfg.Recipient = false, false, true, "ops@example.test"
	require.NoError(t, svc.SaveConfig(context.Background(), cfg), "pool alone is a valid event selection")
	cfg, err = svc.SaveNotificationSettings(context.Background(), AccountOpsNotificationSettings{EmailName: ptrPool("renamed")})
	require.NoError(t, err)
	require.True(t, cfg.Allows(AccountOpsPoolKind))
	require.False(t, cfg.Allows("balance_low"))
	require.False(t, cfg.Allows("unknown"))
	cfg.Enabled = false
	require.False(t, cfg.Allows(AccountOpsPoolKind))
}

func ptrPool[T any](v T) *T { return &v }

func TestAccountOpsPoolMessageIsBoundedAndEscaped(t *testing.T) {
	accounts := []AccountOpsPoolAccount{}
	for i := 0; i < 50; i++ {
		a := poolFixture()[0]
		a.ID = int64(i + 1)
		a.Name = "<script>\n" + strings.Repeat("a", 100)
		a.Status = StatusError
		a.ErrorMessage = "private-upstream-error"
		accounts = append(accounts, a)
	}
	baseline, event, err := ObserveAccountOpsPool("", accounts, AccountOpsPoolConfig{Enabled: true, NotifyStates: true})
	require.NoError(t, err)
	require.Len(t, event.Details.Pool.Changes, 10)
	require.Equal(t, 40, event.Details.Pool.Omitted)
	require.NotContains(t, baseline, "private-upstream-error")
	svc := NewAccountOpsService(nil, nil, nil)
	message := svc.notificationMessage(event)
	require.Contains(t, message.plain, "0 / 50")
	require.NotContains(t, message.html, "<script>")
	require.Contains(t, message.html, "&lt;script&gt;")
	payload, err := accountOpsCustomPayload(`{"title":"{{title}}","body":"{{message}}","group":"Sub2API"}`, message)
	require.NoError(t, err)
	require.Contains(t, payload["body"], "0 / 50")
	_, err = json.Marshal(payload)
	require.NoError(t, err)
}

func TestAccountOpsPoolUsesExistingDeliveryRetries(t *testing.T) {
	settings, repo, sender := &accountOpsSettingsStub{}, &accountOpsRepoStub{}, &accountOpsSenderStub{}
	svc := NewAccountOpsService(settings, repo, sender)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled, cfg.Recipient = true, "ops@example.test"
	cfg.PoolNotifications = &AccountOpsPoolConfig{Enabled: true, NotifyCounts: true}
	cfg.Webhooks = []AccountOpsWebhook{{ID: "robot", Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	calls, body := 0, `{"code":1}`
	svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, e, err := ObserveAccountOpsPool("", poolFixture(), *cfg.PoolNotifications)
	require.NoError(t, err)
	e.Attempts = 1
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, "failed", repo.state)
	require.Equal(t, 5*time.Minute, repo.delay)
	require.Contains(t, sender.body, "1 / 2")
	body, e.Attempts = `{"code":0}`, 2
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, 1, sender.calls, "successful email must not resend")
	require.Equal(t, 2, calls)
	require.Equal(t, "sent", repo.state)
	cfg.PoolNotifications.Enabled = false
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, "suppressed", repo.state)
	require.Equal(t, 2, calls)
}
