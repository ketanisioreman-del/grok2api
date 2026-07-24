package gateway

import (
	"log/slog"
	"sort"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

// activeSetSelectorConfig 控制 Build 固定活跃窗口：
// 仅在窗口内雨露均沾，满额/冷却后从后备池补位。
type activeSetSelectorConfig struct {
	enabled bool
	size    int
}

type activeSetKey struct {
	provider      account.Provider
	upstreamModel string
	quotaMode     string
}

const (
	defaultActiveSetSize = 20
	minActiveSetSize     = 1
	maxActiveSetSize     = 200
)

func defaultActiveSetSelectorConfig() activeSetSelectorConfig {
	return activeSetSelectorConfig{enabled: true, size: defaultActiveSetSize}
}

func normalizeActiveSetSelectorConfig(config activeSetSelectorConfig) activeSetSelectorConfig {
	if config.size <= 0 {
		config.size = defaultActiveSetSize
	}
	if config.size < minActiveSetSize {
		config.size = minActiveSetSize
	}
	if config.size > maxActiveSetSize {
		config.size = maxActiveSetSize
	}
	return config
}

// restrictToActiveSet 将候选收敛到固定活跃窗口。
// 窗口内号不可用时自动从后备健康号补位，始终优先维持 size 个活跃号。
func (s *Selector) restrictToActiveSet(provider account.Provider, upstreamModel, quotaMode string, values []account.RoutingCandidate, indexes []int) []int {
	config := s.activeSetSelectorConfigSnapshot()
	if !config.enabled || config.size <= 0 || len(indexes) == 0 {
		return indexes
	}
	// 健康候选不超过窗口时无需裁剪。
	if len(indexes) <= config.size {
		return indexes
	}

	key := activeSetKey{provider: provider, upstreamModel: upstreamModel, quotaMode: quotaMode}
	eligibleIDs := make(map[uint64]int, len(indexes))
	for _, index := range indexes {
		eligibleIDs[values[index].Credential.ID] = index
	}

	s.activeSetMu.Lock()
	defer s.activeSetMu.Unlock()
	if s.activeSets == nil {
		s.activeSets = make(map[activeSetKey][]uint64)
	}

	previous := s.activeSets[key]
	kept := make([]uint64, 0, config.size)
	keptIndexes := make([]int, 0, config.size)
	for _, id := range previous {
		index, ok := eligibleIDs[id]
		if !ok {
			continue
		}
		kept = append(kept, id)
		keptIndexes = append(keptIndexes, index)
		delete(eligibleIDs, id)
		if len(kept) >= config.size {
			break
		}
	}

	removed := len(previous) - len(kept)
	if len(kept) < config.size {
		// 后备补位：按最近最少选中 + 剩余额度优先，保证窗口内雨露均沾。
		type reserveItem struct {
			id    uint64
			index int
		}
		reserve := make([]reserveItem, 0, len(eligibleIDs))
		for id, index := range eligibleIDs {
			reserve = append(reserve, reserveItem{id: id, index: index})
		}
		s.selectionMu.RLock()
		sort.SliceStable(reserve, func(i, j int) bool {
			left := values[reserve[i].index]
			right := values[reserve[j].index]
			leftSelected := s.lastSelectedAt[left.Credential.ID]
			rightSelected := s.lastSelectedAt[right.Credential.ID]
			if !leftSelected.Equal(rightSelected) {
				return leftSelected.Before(rightSelected)
			}
			leftRemaining, rightRemaining := 0.0, 0.0
			if left.Billing != nil {
				leftRemaining = left.Billing.Remaining()
			}
			if right.Billing != nil {
				rightRemaining = right.Billing.Remaining()
			}
			if leftRemaining != rightRemaining {
				return leftRemaining > rightRemaining
			}
			if left.Credential.Priority != right.Credential.Priority {
				return left.Credential.Priority > right.Credential.Priority
			}
			return left.Credential.ID < right.Credential.ID
		})
		s.selectionMu.RUnlock()

		added := 0
		for _, item := range reserve {
			if len(kept) >= config.size {
				break
			}
			kept = append(kept, item.id)
			keptIndexes = append(keptIndexes, item.index)
			added++
		}
		if removed > 0 || added > 0 || len(previous) == 0 {
			slog.Info("build_active_set_updated",
				"provider", provider,
				"model", upstreamModel,
				"quota_mode", quotaMode,
				"active_size", len(kept),
				"target_size", config.size,
				"removed", removed,
				"added", added,
				"eligible", len(indexes),
			)
		}
	}

	s.activeSets[key] = kept
	if len(keptIndexes) == 0 {
		return indexes
	}
	return keptIndexes
}
