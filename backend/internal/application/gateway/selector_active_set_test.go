package gateway

import (
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestRestrictToActiveSetKeepsFixedWindowAndRefills(t *testing.T) {
	selector := NewSelector(nil, nil, nil, nil, time.Hour, time.Second, time.Minute)
	selector.UpdateActiveSetSelector(true, 3)

	values := make([]account.RoutingCandidate, 6)
	indexes := make([]int, 0, len(values))
	for i := range values {
		values[i] = account.RoutingCandidate{
			Credential: account.Credential{ID: uint64(i + 1), Provider: account.ProviderBuild, Priority: 1},
		}
		indexes = append(indexes, i)
	}

	first := selector.restrictToActiveSet(account.ProviderBuild, "grok-3", "weekly", values, indexes)
	if len(first) != 3 {
		t.Fatalf("first active set size = %d, want 3", len(first))
	}
	firstIDs := map[uint64]struct{}{}
	for _, index := range first {
		firstIDs[values[index].Credential.ID] = struct{}{}
	}

	// 再次选号：活跃集应保持稳定。
	second := selector.restrictToActiveSet(account.ProviderBuild, "grok-3", "weekly", values, indexes)
	if len(second) != 3 {
		t.Fatalf("second active set size = %d, want 3", len(second))
	}
	for _, index := range second {
		if _, ok := firstIDs[values[index].Credential.ID]; !ok {
			t.Fatalf("active set changed without removals: got %d", values[index].Credential.ID)
		}
	}

	// 模拟窗口内 1 个号满额离开候选，应从后备补 1 个。
	remaining := make([]int, 0, 5)
	removedID := values[first[0]].Credential.ID
	for _, index := range indexes {
		if values[index].Credential.ID == removedID {
			continue
		}
		remaining = append(remaining, index)
	}
	refilled := selector.restrictToActiveSet(account.ProviderBuild, "grok-3", "weekly", values, remaining)
	if len(refilled) != 3 {
		t.Fatalf("refilled active set size = %d, want 3", len(refilled))
	}
	for _, index := range refilled {
		if values[index].Credential.ID == removedID {
			t.Fatalf("removed account %d still in active set", removedID)
		}
	}
	// 原窗口剩余 2 个应继续保留。
	kept := 0
	for _, index := range refilled {
		if _, ok := firstIDs[values[index].Credential.ID]; ok {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("kept previous members = %d, want 2", kept)
	}
}

func TestRestrictToActiveSetDisabledReturnsAll(t *testing.T) {
	selector := NewSelector(nil, nil, nil, nil, time.Hour, time.Second, time.Minute)
	selector.UpdateActiveSetSelector(false, 2)
	values := []account.RoutingCandidate{
		{Credential: account.Credential{ID: 1}},
		{Credential: account.Credential{ID: 2}},
		{Credential: account.Credential{ID: 3}},
	}
	indexes := []int{0, 1, 2}
	got := selector.restrictToActiveSet(account.ProviderBuild, "m", "", values, indexes)
	if len(got) != 3 {
		t.Fatalf("disabled active set should keep all candidates, got %d", len(got))
	}
}

func TestBillingIsExhaustedWhenMonthlyUsagePercentFull(t *testing.T) {
	if !(account.Billing{MonthlyLimit: 100, Used: 99.5, CreditUsagePercent: 100}).IsExhausted(0) {
		t.Fatal("monthly 100% usage should be exhausted even with floating remaining")
	}
	if (account.Billing{CreditUsagePercent: 100}).IsExhausted(0) {
		t.Fatal("bare creditUsagePercent without paid/period signal should not exhaust")
	}
}
