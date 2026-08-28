<template>
  <AppLayout title="策略配置" subtitle="所有开关与阈值都在这里；运营配置与系统级规则分开管理">
    <div v-if="!form" class="card p-12 text-center text-sm text-gray-500 dark:text-dark-400">
      正在加载策略…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <SegmentedControl
          v-model="tab"
          :options="[
            { value: 'ops', label: '运营配置', icon: 'cog' },
            { value: 'system', label: '系统级规则', icon: 'brain' },
            { value: 'scope', label: '守护范围', icon: 'grid' }
          ]"
        />
        <div class="flex items-center gap-2">
          <button type="button" class="btn btn-ghost btn-sm" @click="resetForm">放弃修改</button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="guardian.busy" @click="save">
            <Icon name="check" size="sm" />
            保存策略
          </button>
        </div>
      </div>

      <!-- 运营配置 -->
      <template v-if="tab === 'ops'">
        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">全局默认策略</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              分组没有单独设置时使用它；分组级选择在「分组调度」页。
            </p>
          </div>
          <div class="space-y-4 p-6">
            <SegmentedControl
              v-model="form.strategy"
              :options="[
                { value: 'price', label: '价格优先', icon: 'dollar' },
                { value: 'speed', label: '速度优先', icon: 'bolt' },
                { value: 'balanced', label: '均衡', icon: 'swap' }
              ]"
            />
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <Field v-model="form.weights.budget" label="每组权重预算" type="number" :min="1" />
              <Field
                v-model="form.weights.min_priority"
                label="自动优先级下限"
                type="number"
                :min="1"
                hint="自动调权不会把 priority 调整到此值以下；手动修改不受影响"
              />
              <Field
                v-model="form.weights.gate_floor"
                label="权重健康闸门"
                type="number"
                :min="0"
                :max="100"
                hint="健康分低于该值权重归零"
              />
              <Field
                v-model="form.weights.balanced_price_ratio"
                label="均衡中价格占比"
                type="number"
                :min="0"
                :max="1"
                :step="0.05"
              />
              <Field
                v-model="form.upstream_multiplier.interval_seconds"
                label="上游倍率拉取间隔"
                suffix="秒"
                type="number"
                :min="30"
                hint="对所有已开启实时倍率的 API Key 渠道生效"
              />
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">自动执行范围</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              关闭某一项后 Guardian 只计算期望值、不写回 sub2api，便于先观察再放权。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-3 p-6 sm:grid-cols-2">
            <SwitchRow
              v-model="form.auto_apply.schedulable"
              label="熔断 / 恢复调度"
              description="写 sub2api 的 schedulable 字段"
            />
            <SwitchRow
              v-model="form.auto_apply.priority"
              label="优先级调权"
              description="写 priority，数值越小越优先"
            />
            <SwitchRow
              v-model="form.auto_apply.load_factor"
              label="负载因子调权"
              description="写 load_factor，承载权重比例"
            />
            <SwitchRow
              v-model="form.auto_apply.concurrency"
              label="并发扩缩容"
              description="写 concurrency，需要同时开启智能扩容"
            />
          </div>
        </section>

        <section class="card">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">熔断</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                致命错误立即熔断；错误率与延迟触发软熔断，且受保底池与每轮切换上限约束。
              </p>
            </div>
            <Toggle v-model="form.breaker.enabled" />
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field v-model="form.breaker.http_window" label="错误率窗口" suffix="次请求" type="number" :min="1" />
            <Field v-model="form.breaker.http_failures" label="窗口内失败次数" suffix="次" type="number" :min="1" />
            <Field
              v-model="form.breaker.http_score_below"
              label="且健康分低于"
              suffix="分"
              type="number"
              :min="0"
              :max="100"
            />
            <Field v-model="form.breaker.latency_window" label="延迟窗口" suffix="次请求" type="number" :min="1" />
            <Field
              v-model="form.breaker.latency_occurrences"
              label="窗口内慢响应次数"
              suffix="次"
              type="number"
              :min="1"
            />
            <Field v-model="form.breaker.latency_ttfb_ms" label="慢响应首字界限" suffix="ms" type="number" :min="100" />
            <Field
              v-model="form.breaker.max_switch_per_round"
              label="每轮最多熔断"
              suffix="个"
              type="number"
              :min="1"
              hint="防雪崩：一轮里最多切换几个渠道"
            />
            <Field
              v-model="form.breaker.fused_cooldown_seconds"
              label="熔断冷却"
              suffix="秒"
              type="number"
              :min="0"
              hint="冷却期内不考虑回池"
            />
            <div class="sm:col-span-2 lg:col-span-4">
              <Field
                v-model="instantFuseCodesText"
                label="见到即熔断的错误码（逗号分隔）"
                placeholder="例如 402, 429"
                hint="留空则走上面的错误率与延迟判定。填写后最近一次请求命中即熔断，适合把「继续打也没意义」的错误快速摘掉；仍受保底池约束"
              />
            </div>
            <div class="sm:col-span-2 lg:col-span-4">
              <SwitchRow
                v-model="form.breaker.hard_fatal"
                label="凭据失效立即熔断"
                description="仅指认证失败这类不会自行恢复的错误；限流与额度耗尽不算在内，它们等窗口重置就能恢复。仍受保底池约束，不会打空分组"
              />
            </div>
            <div class="sm:col-span-2 lg:col-span-4">
              <SwitchRow
                v-model="form.breaker.http_degrade_only"
                label="网关错误只降级不熔断"
                description="5xx 多为上游临时抖动，摘掉渠道等于减少可用容量。开启后这类渠道仍参与调度，但权重与优先级被压低，流量自然挪走"
              />
            </div>
            <div class="sm:col-span-2 lg:col-span-4">
              <div
                class="flex items-start gap-2 rounded-xl border border-primary-200 bg-primary-50 px-3 py-2.5 text-xs text-primary-800 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-200"
              >
                <Icon name="infoCircle" size="sm" class="mt-0.5 flex-shrink-0" />
                <div>
                  <p class="font-medium">限流（429 / 额度耗尽）永不摘除调度</p>
                  <p class="mt-1">
                    这条不受任何配置影响，包括「见到即熔断」的错误码与自动处置。
                    sub2api 自己已经用 <code>rate_limit_reset_at</code> 把限流账号排除在选路之外、
                    窗口一过自动恢复；Guardian 再改可调度状态，就会把「到点自动恢复」
                    变成「要等恢复探测跑成功才回来」，高并发时白白损失容量。
                    限流只压低权重，渠道始终留在池子里。
                  </p>
                </div>
              </div>
            </div>
            <div class="sm:col-span-2 lg:col-span-4">
              <SwitchRow
                v-model="form.breaker.latency_degrade_only"
                label="延迟超标只降级不熔断"
                description="慢渠道仍然可用，压低权重比直接摘掉更划算"
              />
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">保底与降级</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              保底池保证每个分组不会断供：熔断会让可用渠道低于下限时，改为「保底强留」并告警。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field
              v-model="form.breaker.min_pool_size"
              label="保底可用渠道数"
              suffix="个"
              type="number"
              :min="0"
            />
            <Field
              v-model="form.breaker.min_pool_score"
              label="计入可用池的最低分"
              suffix="分"
              type="number"
              :min="0"
              :max="100"
            />
            <Field
              v-model="form.degrade.score_threshold"
              label="降级线"
              suffix="分"
              type="number"
              :min="0"
              :max="100"
            />
            <Field v-model="form.degrade.priority_step" label="降级优先级步进" type="number" :min="1" />
            <Field
              v-model="form.degrade.load_factor_ratio"
              label="降级负载乘数"
              type="number"
              :min="0.05"
              :max="1"
              :step="0.05"
            />
            <Field v-model="form.degrade.min_load_factor" label="最低负载因子" type="number" :min="1" />
            <div class="sm:col-span-2 lg:col-span-2">
              <SwitchRow v-model="form.degrade.enabled" label="启用降级" description="低分渠道压低权重但不停止调度" />
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">健康回池</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                熔断渠道按低频探测恢复，达标后自动还原优先级与负载并重新上线。
              </p>
              <p class="mt-0.5 text-xs text-gray-400 dark:text-dark-500">
                恢复探测独立于下方的「主动探测」总开关：熔断渠道拿不到真实流量，
                探测是它唯一的复活途径。关掉这个开关，熔断渠道将只能人工恢复。
              </p>
            </div>
            <Toggle v-model="form.recovery.enabled" />
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field
              v-model="form.recovery.probe_interval_seconds"
              label="恢复探测间隔"
              suffix="秒"
              type="number"
              :min="30"
            />
            <Field v-model="form.recovery.target_score" label="回池目标分" suffix="分" type="number" :min="0" :max="100" />
            <Field v-model="form.recovery.success_count" label="连续成功次数" suffix="次" type="number" :min="1" />
            <Field v-model="form.recovery.hold_seconds" label="健康持续时长" suffix="秒" type="number" :min="0" />
          </div>
        </section>

        <section class="card">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">负载因子调权</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                权重变化小于阈值不写回，写回后进入冷却期，避免路由反复震荡。
              </p>
            </div>
            <Toggle v-model="form.weights.enabled" />
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field
              v-model="form.weights.change_threshold"
              label="微调防抖阈值"
              type="number"
              :min="0.01"
              :max="1"
              :step="0.01"
              hint="0.1 表示变化不足 10% 不写回"
            />
            <Field v-model="form.weights.cooldown_seconds" label="调整冷却" suffix="秒" type="number" :min="0" />
            <Field v-model="form.weights.min_load_factor" label="负载因子下限" type="number" :min="1" />
            <Field v-model="form.weights.max_load_factor" label="负载因子上限" type="number" :min="1" />
            <Field v-model="form.weights.price_exp" label="价格权重强度" type="number" :min="0.1" :step="0.1" />
            <Field v-model="form.weights.speed_exp" label="速度权重强度" type="number" :min="0.1" :step="0.1" />
          </div>
        </section>

        <section class="card" :class="form.cleanup.enabled && dangerousCleanup && 'border-red-300 dark:border-red-900/60'">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">认证失效自动处置</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                渠道反复出现 401 / 403 等凭据失效错误时自动处置。余额不足、额度耗尽这类
                「充值即可恢复」的错误不会触发。
              </p>
            </div>
            <Toggle v-model="form.cleanup.enabled" />
          </div>

          <div v-if="form.cleanup.enabled" class="space-y-4 p-6">
            <div>
              <p class="input-label">处置动作</p>
              <SegmentedControl
                v-model="form.cleanup.action"
                :options="[
                  { value: 'pause', label: '暂停调度' },
                  { value: 'disable', label: '停用账号' },
                  { value: 'delete', label: '删除账号' }
                ]"
              />
              <p class="input-hint">
                {{ cleanupActionHint }}
              </p>
            </div>

            <div
              v-if="dangerousCleanup"
              class="rounded-lg border border-red-200 bg-red-50 p-4 text-sm dark:border-red-800 dark:bg-red-900/20"
            >
              <div class="flex items-start gap-3">
                <Icon name="exclamationTriangle" size="md" class="mt-0.5 flex-shrink-0 text-red-500" />
                <div class="space-y-1 text-red-700 dark:text-red-300">
                  <p class="font-medium">删除不可撤销</p>
                  <p>
                    sub2api 的账号接口对 API Key 做了脱敏，Guardian 读不到凭据，
                    <strong>删除后无法由 Guardian 重建</strong>，只能从你自己的凭据来源恢复。
                    事件日志会记录被删渠道的名称、平台、分组与倍率，但不含 Key。
                  </p>
                  <p>如果只是想让它别再接流量，用「暂停调度」或「停用账号」更稳妥。</p>
                </div>
              </div>
            </div>

            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <Field
                v-model="form.cleanup.window"
                label="判定窗口"
                suffix="次样本"
                type="number"
                :min="1"
              />
              <Field
                v-model="form.cleanup.occurrences"
                label="窗口内失效次数"
                suffix="次"
                type="number"
                :min="1"
                hint="达到该次数才触发"
              />
              <Field
                v-model="form.cleanup.min_fused_minutes"
                label="最短观察时长"
                suffix="分钟"
                type="number"
                :min="0"
                hint="渠道进入当前状态需持续这么久才处置，给人工介入留窗口。设为 0 表示立即处置"
              />
              <Field
                v-model="form.cleanup.max_per_round"
                label="每轮最多处置"
                suffix="个"
                type="number"
                :min="1"
                hint="防止配置失误清空整个池子"
              />
            </div>

            <Field
              v-model="cleanupStatusCodesText"
              label="触发处置的错误码（逗号分隔）"
              placeholder="例如 401, 403"
              hint="留空则按下面的「仅处置凭据失效」判定；填写后只认这些状态码。判定不要求渠道先被熔断 —— 命中次数达标、满足观察期、且不是分组内最后一个即可处置（删除前会先摘掉流量）"
            />

            <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <SwitchRow
                v-model="form.cleanup.keep_last_in_group"
                label="保留分组内最后一个渠道"
                description="即使满足条件也绝不处置，避免分组彻底断供"
              />
              <SwitchRow
                v-model="form.cleanup.only_auth_errors"
                label="仅处置凭据失效"
                :description="
                  form.cleanup.trigger_status_codes.length
                    ? '已配置错误码，此开关不生效'
                    : '关闭后余额不足、额度耗尽也会触发，请谨慎'
                "
                :disabled="form.cleanup.trigger_status_codes.length > 0"
              />
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">智能扩容</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                负载率超过阈值时小步提升账号并发，状态不佳的渠道先缩容。
              </p>
            </div>
            <Toggle v-model="form.scaling.enabled" />
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field
              v-model="form.scaling.global_max_concurrency"
              label="全局并发上限"
              type="number"
              :min="1"
            />
            <Field v-model="form.scaling.min_per_account" label="单账号并发下限" type="number" :min="1" />
            <Field v-model="form.scaling.max_per_account" label="单账号并发上限" type="number" :min="1" />
            <Field
              v-model="form.scaling.scale_up_ratio"
              label="扩容触发负载率"
              type="number"
              :min="0.1"
              :max="1"
              :step="0.05"
            />
            <Field v-model="form.scaling.step_up" label="扩容步长" type="number" :min="1" />
            <Field v-model="form.scaling.step_down" label="缩容步长" type="number" :min="1" />
            <Field v-model="form.scaling.cooldown_seconds" label="扩缩容冷却" suffix="秒" type="number" :min="0" />
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">采样（测活 / 测延迟）</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              真实流量优先、探针兜底：有新鲜真实样本时可跳过探测，减少上游压力。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
            <Field v-model="form.probe.interval_seconds" label="探测间隔" suffix="秒" type="number" :min="30" />
            <Field v-model="form.probe.timeout_seconds" label="探测超时" suffix="秒" type="number" :min="5" />
            <Field v-model="form.probe.concurrency" label="探测并发" type="number" :min="1" :max="32" />
            <Field v-model="form.probe.traffic_fresh_seconds" label="真实样本新鲜期" suffix="秒" type="number" :min="10" />
            <Field v-model="form.probe.model" label="全局测活模型" placeholder="留空则用账号默认模型" />
            <Field v-model="form.probe.prompt" label="测活提示词" />
            <Field v-model="form.traffic.refresh_seconds" label="流量拉取间隔" suffix="秒" type="number" :min="10" />
            <Field
              v-model="form.traffic.lookback_minutes"
              label="流量回溯窗口"
              suffix="分钟"
              type="number"
              :min="5"
            />
            <div class="sm:col-span-2">
              <SwitchRow
                v-model="form.probe.enabled"
                label="启用主动探测"
                description="只影响未熔断渠道的常规巡检；熔断渠道的恢复探测由「健康回池」开关单独控制"
              />
            </div>
            <div class="sm:col-span-2">
              <SwitchRow
                v-model="form.probe.skip_when_traffic_fresh"
                label="有新鲜真实流量时跳过探测"
                description="真实请求已经能反映健康度，无需额外探针"
              />
            </div>
            <div class="sm:col-span-2">
              <SwitchRow
                v-model="form.traffic.enabled"
                label="接入真实流量样本"
                description="需要 sub2api 开启运维监控（ops）"
              />
            </div>
            <Field
              v-model="form.traffic.max_samples_per_account"
              label="每渠道最多取样"
              suffix="条"
              type="number"
              :min="5"
              :max="200"
            />
          </div>
        </section>
      </template>

      <!-- 系统级规则 -->
      <template v-else-if="tab === 'system'">
        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">健康分公式</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              短期分取最近 N 次，最新一次占固定权重，其余按几何衰减；最终分 = 短期 × 占比 + 长期 × 剩余。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3">
            <Field v-model="form.scoring.short_window" label="短期窗口" suffix="次" type="number" :min="1" />
            <Field v-model="form.scoring.long_window" label="长期窗口" suffix="次" type="number" :min="1" />
            <Field
              v-model="form.scoring.latest_weight"
              label="最新一次权重"
              type="number"
              :min="0.05"
              :max="1"
              :step="0.05"
            />
            <Field
              v-model="form.scoring.short_ratio"
              label="短期分占比"
              type="number"
              :min="0.05"
              :max="1"
              :step="0.05"
            />
            <Field
              v-model="form.scoring.slow_ttfb_ms"
              label="首字慢阈值"
              suffix="ms"
              type="number"
              :min="100"
              hint="超过该首字时间记为「首字慢」而非「完美健康」"
            />
          </div>
          <div class="border-t border-gray-100 px-6 py-4 text-sm text-gray-600 dark:border-dark-700 dark:text-dark-300">
            当前公式：最终分 = 短期分 × {{ form.scoring.short_ratio }} + 长期分 ×
            {{ (1 - form.scoring.short_ratio).toFixed(2) }}
            （短期取最近 {{ form.scoring.short_window }} 次，最新一次权重
            {{ (form.scoring.latest_weight * 100).toFixed(0) }}%；长期取最近
            {{ form.scoring.long_window }} 次均值）
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">事件分值表</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              每类结果对应的健康分，致命错误固定为一票否决。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3">
            <Field v-model="form.scoring.event_scores.perfect" label="完美健康" type="number" :min="0" :max="100" />
            <Field v-model="form.scoring.event_scores.slow_ttfb" label="首字慢" type="number" :min="0" :max="100" />
            <Field
              v-model="form.scoring.event_scores.upstream_unknown"
              label="上游未知异常"
              type="number"
              :min="0"
              :max="100"
            />
            <Field
              v-model="form.scoring.event_scores.gateway_error"
              label="网关错误 (5xx)"
              type="number"
              :min="0"
              :max="100"
            />
            <Field
              v-model="form.scoring.event_scores.quota_exhausted"
              label="限流 / 额度耗尽"
              type="number"
              :min="1"
              :max="100"
              hint="不要设为 0：额度会随窗口重置恢复，0 分低于回池目标分会让渠道永远回不了池"
            />
            <Field v-model="form.scoring.event_scores.probe_fail" label="探测失败" type="number" :min="0" :max="100" />
            <Field
              v-model="form.scoring.event_scores.fatal"
              label="凭据失效"
              type="number"
              :min="0"
              :max="100"
              hint="仅指认证失败这类不会自行恢复的错误"
            />
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">错误分类</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              命中关键字的错误按致命错误处理；列出的状态码按网关 / 限流错误处理。
            </p>
          </div>
          <div class="space-y-4 p-6">
            <label class="block">
              <span class="input-label">致命错误关键字（每行一个）</span>
              <textarea v-model="fatalPatternsText" class="input font-mono text-xs" rows="8" />
              <span class="input-hint">401 / 402 / 403 状态码始终判定为致命错误，无需在此重复。</span>
            </label>
            <Field v-model="gatewayCodesText" label="网关错误状态码（逗号分隔）" />
          </div>
        </section>
      </template>

      <!-- 守护范围 -->
      <template v-else>
        <section class="card">
          <div class="card-header flex items-center justify-between">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">参与守护的分组</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                未参与的分组不会被探测、熔断或调权。
              </p>
            </div>
            <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300">
              <Toggle :model-value="allGroups" @update:model-value="setAllGroups" />
              全部分组
            </label>
          </div>
          <div class="p-6">
            <div v-if="allGroups" class="text-sm text-gray-500 dark:text-dark-400">
              当前所有分组都参与守护。关闭上方开关可只勾选部分分组。
            </div>
            <div v-else class="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
              <label
                v-for="group in guardian.groups"
                :key="group.id"
                class="flex cursor-pointer items-center gap-3 rounded-xl border border-gray-100 px-3 py-2 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800/50"
              >
                <input
                  type="checkbox"
                  class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                  :checked="form.managed_group_ids.includes(group.id)"
                  @change="toggleGroup(group.id)"
                />
                <span class="min-w-0">
                  <span class="block truncate text-sm text-gray-900 dark:text-white">{{ group.name }}</span>
                  <span class="block truncate text-xs text-gray-500 dark:text-dark-400">
                    #{{ group.id }} · {{ group.platform || '全平台' }}
                  </span>
                </span>
              </label>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">账号类型与平台</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              留空表示不限；填写后只有匹配的账号类型 / 平台会被守护。
            </p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2">
            <Field
              v-model="accountTypesText"
              label="账号类型"
              placeholder="apikey, oauth, setup_token…"
              hint="逗号分隔，留空表示全部类型"
            />
            <Field
              v-model="platformsText"
              label="平台"
              placeholder="anthropic, openai, gemini…"
              hint="逗号分隔，留空表示全部平台"
            />
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">排除的分组</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              被排除的分组整组移出调度系统管控：不探测、不熔断、不调权，现有配置保持不动。
              优先级高于「参与守护的分组」勾选，也高于分组自身的启用开关。
            </p>
          </div>
          <div class="space-y-2 p-6">
            <Field
              v-model="excludedGroupsText"
              label="排除的分组 ID（逗号分隔）"
              placeholder="例如 3, 5"
              hint="也可以在「分组调度」页用每张卡上的「排除分组」按钮操作"
            />
            <div v-if="excludedGroups.length" class="flex flex-wrap gap-1.5">
              <Badge v-for="group in excludedGroups" :key="group.id" tone="danger">
                #{{ group.id }} {{ group.name }}
              </Badge>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">暂停与排除的渠道</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              暂停的渠道不接流量但继续监控计分，且不会被健康分回升自动恢复；
              排除的渠道则完全不参与，并会恢复接管前的配置。
            </p>
          </div>
          <div class="space-y-4 p-6">
            <div class="space-y-2">
              <Field v-model="pausedText" label="暂停调度的渠道 ID（逗号分隔）" placeholder="例如 12, 34" />
              <div v-if="pausedChannels.length" class="flex flex-wrap gap-1.5">
                <Badge v-for="channel in pausedChannels" :key="channel.id" tone="warning">
                  #{{ channel.id }} {{ channel.name }}
                </Badge>
              </div>
            </div>
            <div class="space-y-2">
              <Field v-model="excludedText" label="排除的渠道 ID（逗号分隔）" placeholder="例如 12, 34" />
              <div v-if="excludedChannels.length" class="flex flex-wrap gap-1.5">
                <Badge v-for="channel in excludedChannels" :key="channel.id" tone="gray">
                  #{{ channel.id }} {{ channel.name }}
                </Badge>
              </div>
            </div>
          </div>
        </section>

        <section class="card border-red-200 dark:border-red-900/50">
          <div class="card-header">
            <h2 class="text-base font-semibold text-red-600 dark:text-red-400">交还控制权</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              把所有被 Guardian 改动过的渠道恢复为接管前的优先级、负载因子、并发与调度状态。
            </p>
          </div>
          <div class="p-6">
            <button type="button" class="btn btn-danger" :disabled="guardian.busy" @click="restoreAll">
              <Icon name="refresh" size="sm" />
              恢复全部渠道原始配置
            </button>
          </div>
        </section>
      </template>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import Field from '@/components/Field.vue'
import Toggle from '@/components/Toggle.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import type { Policy } from '@/lib/types'

const guardian = useGuardianStore()
const ui = useUIStore()

// 支持从别处直接跳到某个页签，例如分组页的「去守护范围」链接。
const validTabs = ['ops', 'system', 'scope']
const requestedTab = String(useRoute().query.tab ?? '')
const tab = ref(validTabs.includes(requestedTab) ? requestedTab : 'ops')
const form = ref<Policy | null>(null)

onMounted(async () => {
  try {
    form.value = clone(await guardian.loadPolicy())
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
})

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

const dangerousCleanup = computed(() => form.value?.cleanup.action === 'delete')

const cleanupActionHint = computed(() => {
  switch (form.value?.cleanup.action) {
    case 'pause':
      return '加入暂停名单：不接流量但继续监控，不会被健康分回升自动放回，等你手动恢复。'
    case 'disable':
      return '在 sub2api 里把账号置为停用：保留凭据，可在 sub2api 后台改回来。'
    case 'delete':
      return '从 sub2api 删除账号：不可撤销，凭据无法找回。'
    default:
      return ''
  }
})

const allGroups = computed(() => form.value?.managed_group_mode !== 'selected')

function setAllGroups(value: boolean) {
  if (!form.value) return
  if (value) {
    form.value.managed_group_mode = 'all'
    form.value.managed_group_ids = []
    return
  }
  form.value.managed_group_mode = 'selected'
  if (!form.value.managed_group_ids.length) {
    form.value.managed_group_ids = guardian.groups.map(group => group.id)
  }
}

function toggleGroup(id: number) {
  if (!form.value) return
  const ids = form.value.managed_group_ids
  form.value.managed_group_ids = ids.includes(id) ? ids.filter(item => item !== id) : [...ids, id]
}

const fatalPatternsText = computed({
  get: () => form.value?.classify.fatal_patterns.join('\n') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.classify.fatal_patterns = value
      .split('\n')
      .map(item => item.trim())
      .filter(Boolean)
  }
})

const gatewayCodesText = computed({
  get: () => form.value?.classify.gateway_status_codes.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.classify.gateway_status_codes = String(value)
      .split(',')
      .map(item => Number(item.trim()))
      .filter(item => Number.isInteger(item) && item > 0)
  }
})

const accountTypesText = computed({
  get: () => form.value?.managed_account_types.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.managed_account_types = splitList(String(value))
  }
})

const platformsText = computed({
  get: () => form.value?.managed_platforms.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.managed_platforms = splitList(String(value))
  }
})

const excludedText = computed({
  get: () => form.value?.excluded_account_ids.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.excluded_account_ids = parseIDList(String(value))
  }
})

const pausedText = computed({
  get: () => form.value?.paused_account_ids.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.paused_account_ids = parseIDList(String(value))
  }
})

const excludedGroupsText = computed({
  get: () => form.value?.excluded_group_ids.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.excluded_group_ids = parseIDList(String(value))
  }
})

// 见到即熔断的状态码。留空则走常规的错误率与延迟判定。
const instantFuseCodesText = computed({
  get: () => form.value?.breaker.instant_status_codes.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.breaker.instant_status_codes = parseStatusCodes(String(value))
  }
})

// 触发自动处置的 HTTP 状态码。留空时回落到「仅处置凭据失效」的判定口径。
const cleanupStatusCodesText = computed({
  get: () => form.value?.cleanup.trigger_status_codes.join(', ') ?? '',
  set(value: string) {
    if (!form.value) return
    form.value.cleanup.trigger_status_codes = parseStatusCodes(String(value))
  }
})

const excludedChannels = computed(() =>
  guardian.channels.filter(channel => form.value?.excluded_account_ids.includes(channel.id))
)

const pausedChannels = computed(() =>
  guardian.channels.filter(channel => form.value?.paused_account_ids.includes(channel.id))
)

const excludedGroups = computed(() =>
  guardian.groups.filter(group => form.value?.excluded_group_ids.includes(group.id))
)

// parseStatusCodes 解析逗号分隔的 HTTP 状态码，剔除非法值。
function parseStatusCodes(value: string): number[] {
  return value
    .split(',')
    .map(item => Number(item.trim()))
    .filter(item => Number.isInteger(item) && item >= 100 && item <= 599)
}

function parseIDList(value: string): number[] {
  return value
    .split(',')
    .map(item => Number(item.trim()))
    .filter(item => Number.isInteger(item) && item > 0)
}

function splitList(value: string): string[] {
  return value
    .split(',')
    .map(item => item.trim())
    .filter(Boolean)
}

async function save() {
  if (!form.value) return

  // 开启自动删除是不可逆配置，保存前显式确认一次。
  if (form.value.cleanup.enabled && form.value.cleanup.action === 'delete') {
    const confirmed = window.confirm(
      '你正在开启「认证失效自动删除渠道」。\n\n' +
        '被删除的账号无法由 Guardian 重建（sub2api 不返回 API Key），' +
        '只能从你自己的凭据来源恢复。\n\n确定要开启吗？'
    )
    if (!confirmed) return
  }

  try {
    const result = await guardian.run(() => api.savePolicy(form.value as Policy))
    form.value = clone(result.policy)
    await guardian.loadPolicy()
    ui.notify('success', '策略已保存并立即生效')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function resetForm() {
  try {
    form.value = clone(await guardian.loadPolicy())
    ui.notify('info', '已放弃未保存的修改')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function restoreAll() {
  if (!window.confirm('确定要把所有渠道恢复为接管前的配置吗？Guardian 会交还控制权。')) return
  try {
    const result = await guardian.run(() => api.restoreAll())
    ui.notify('success', `已恢复 ${result.restored} 个渠道的原始配置`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}
</script>
