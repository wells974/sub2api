package repository

import (
	"testing"
	"time"
)

func TestOAuth429EpochUsesGatewayElapsedTime(t *testing.T) {
	databaseNow := time.Unix(1000, 0)
	epoch := oauth429Epoch{databaseNow: databaseNow, validSince: databaseNow.Add(-time.Second)}
	for _, skew := range []time.Duration{-time.Minute, time.Minute} {
		gatewayNow := databaseNow.Add(skew)
		for _, tc := range []struct {
			name         string
			age          time.Duration
			routingSince time.Time
			want         bool
		}{
			{"after both transitions", time.Millisecond, databaseNow.Add(-time.Second), true},
			{"before account transition", 2 * time.Second, time.Time{}, false},
			{"before routing transition", 2 * time.Millisecond, databaseNow.Add(-time.Millisecond), false},
		} {
			started := gatewayNow.Add(-tc.age)
			if got := epoch.admitsRequestStart(started, gatewayNow, tc.routingSince); got != tc.want {
				t.Errorf("%s with clock offset %s: got %t, want %t", tc.name, skew, got, tc.want)
			}
		}
	}
}
