package engine

import (
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// applyScaling 按负载率小步调整账号并发上限（智能扩容）。
//
// 负载率用「组内已分配并发之和 / 全局并发上限」近似：sub2api 不直接暴露实时并发数，
// 因此这里以配置容量为口径做保守扩缩容，并受单账号区间与全局上限双重约束。
func applyScaling(r *round) {
	for groupID, members := range r.groupMembers {
		p := r.groupPolicy(groupID)
		if !p.Scaling.Enabled {
			continue
		}
		scaleGroup(r, groupID, members, p)
	}
}

func scaleGroup(r *round, groupID int64, members []*channel, p policy.Policy) {
	owned := make([]*channel, 0, len(members))
	allocated := 0
	for _, ch := range members {
		if ch.primaryGroup != groupID {
			continue
		}
		owned = append(owned, ch)
		allocated += ch.account.Concurrency
	}
	if len(owned) == 0 {
		return
	}

	headroom := p.Scaling.GlobalMaxConcurrency - allocated
	loadRatio := 1.0
	if p.Scaling.GlobalMaxConcurrency > 0 {
		loadRatio = float64(allocated) / float64(p.Scaling.GlobalMaxConcurrency)
	}
	scaleUp := loadRatio >= p.Scaling.ScaleUpRatio && headroom > 0

	for _, ch := range owned {
		if !ch.state.CooldownTill.IsZero() && r.now.Before(ch.state.CooldownTill) {
			continue
		}
		current := ch.account.Concurrency
		if current <= 0 {
			current = p.Scaling.MinPerAccount
		}
		target := current

		switch {
		case ch.desired.health == domain.HealthFused ||
			ch.desired.health == domain.HealthExcluded ||
			ch.desired.health == domain.HealthPaused:
			continue
		case ch.desired.health == domain.HealthDegraded || ch.desired.health == domain.HealthSurvivor:
			// 状态不佳的渠道先缩容，把并发让给健康渠道。
			target = current - p.Scaling.StepDown
		case scaleUp:
			step := p.Scaling.StepUp
			if step > headroom {
				step = headroom
			}
			target = current + step
		}

		target = clampInt(target, p.Scaling.MinPerAccount, p.Scaling.MaxPerAccount)
		if target == current {
			continue
		}
		if target > current {
			headroom -= target - current
		}
		value := target
		ch.desired.concurrency = &value
	}
}
