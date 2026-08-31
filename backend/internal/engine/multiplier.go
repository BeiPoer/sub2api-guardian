package engine

import (
	"strconv"

	"sub2api-guardian/backend/internal/policy"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// resolveMultipliers 给每个渠道算出生效的调度倍率。
//
// 优先使用渠道管理凭据联动倍率；没有联动时，API Key 渠道显式开启实时倍率后
// 使用本轮目录同步得到的 sub2api 账号倍率，再回退人工倍率或账号类型默认值。
// 该值只参与价格优先的权重计算，不会写回 sub2api。
func resolveMultipliers(r *round) {
	for _, ch := range r.channels {
		snapshot, hasSnapshot := r.upstreamMultipliers[ch.account.ID]
		ch.state.Multiplier, _ = ch.pol.ResolveMultiplierSnapshot(
			ch.account.ID,
			ch.account.Type,
			ch.account.RateMultiplier,
			snapshot.Value,
			hasSnapshot,
		)
		ch.state.MultiplierManual = ch.pol.HasManualMultiplier(ch.account.ID)
		ch.state.Balance = ch.account.Balance()
	}
}

// 编译期确认默认值符合约定：账号类型必须比 API Key 更优先。
var _ = func() struct{} {
	if policy.DefaultOAuthMultiplier >= policy.DefaultAPIKeyMultiplier {
		panic("账号类型渠道的默认倍率必须低于 API Key，否则价格优先会反向")
	}
	return struct{}{}
}()
