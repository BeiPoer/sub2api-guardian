package engine

import (
	"strconv"

	"sub2api-guardian/backend/internal/policy"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// resolveMultipliers 给每个渠道算出生效的调度倍率。
//
// 倍率是纯本地口径：人工设置优先，否则按账号类型取默认值
// （账号类型渠道 0.01，API Key 1）。它只参与价格优先的权重计算，
// 不读也不写 sub2api 的任何计费字段。
func resolveMultipliers(r *round) {
	for _, ch := range r.channels {
		ch.state.Multiplier = ch.pol.MultiplierFor(ch.account.ID, ch.account.Type)
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
