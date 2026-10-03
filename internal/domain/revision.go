package domain

import (
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// PlanRevision 保存修复方案某一版制备条件及其不可变的有效范围。
//
// 版本号从 1 开始连续追加：第 1 版为审批通过时生效的初始条件，之后每次
// 现场变更追加新版本。版本一经生效，其条件与有效范围都不能再被改写。
type PlanRevision struct {
	ID             string `json:"id"`
	PlanID         string `json:"plan_id"`
	Revision       int    `json:"revision"`
	Objective      string `json:"objective"`
	Method         string `json:"method"`
	Adhesive       string `json:"adhesive"`
	TargetExposure int    `json:"target_exposure"`
	Reason         string `json:"reason,omitempty"`
	ChangedBy      string `json:"changed_by,omitempty"`
	// EffectiveFrom/EffectiveTo 划定该版本条件的生效时间区间；当前有效版本
	// 的 EffectiveTo 始终为 nil，区间在追加下一版时闭合，闭合后不再改变。
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	// FromSequence/ToSequence 以进度序号划定有效范围：该版本适用于序号位于
	// [FromSequence, ToSequence] 的进度记录；当前有效版本的 ToSequence 为 nil。
	FromSequence int       `json:"from_sequence"`
	ToSequence   *int      `json:"to_sequence,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// NewBaselineRevision 创建草稿方案携带的第 1 版条件。该版本在方案审批通过
// 前处于未生效状态（生效区间为空），由 ActivateBaselineRevision 激活。
func NewBaselineRevision(id string, plan PrepPlan, now time.Time) (PlanRevision, error) {
	if id == "" {
		return PlanRevision{}, contracts.Internal("missing_generated_id", "方案版本标识未生成", nil)
	}
	if plan.Status != PlanDraft {
		return PlanRevision{}, contracts.Conflict("revision_baseline_state", "只有草稿方案能够建立初始版本")
	}
	return PlanRevision{
		ID:             id,
		PlanID:         plan.ID,
		Revision:       1,
		Objective:      plan.Objective,
		Method:         plan.Method,
		Adhesive:       plan.Adhesive,
		TargetExposure: plan.TargetExposure,
		CreatedAt:      now.UTC(),
	}, nil
}

// IsPending 表示版本已建立但尚未随方案审批生效。
func (r PlanRevision) IsPending() bool {
	return r.EffectiveFrom == nil
}

// IsCurrent 表示该版本是当前有效版本，尚未被后续版本闭合。
func (r PlanRevision) IsCurrent() bool {
	return r.EffectiveFrom != nil && r.EffectiveTo == nil
}

// Activate 在方案审批通过时激活第 1 版，固定其生效起点。
func (r *PlanRevision) Activate(now time.Time) error {
	if r.Revision != 1 || !r.IsPending() {
		return contracts.Conflict("revision_not_activatable", "只有未生效的初始版本可以激活")
	}
	stamp := now.UTC()
	r.EffectiveFrom = &stamp
	r.FromSequence = 1
	return nil
}

// Close 在追加下一版本时闭合当前有效版本，结束其有效范围。闭合是幂等的：
// 有效范围一旦固定，重复闭合保持原值不变。
func (r *PlanRevision) Close(at time.Time, lastSequence int) error {
	if r.EffectiveTo != nil {
		return nil
	}
	if !r.IsCurrent() {
		return contracts.Conflict("revision_not_current", "只有当前有效版本可以被闭合")
	}
	stamp := at.UTC()
	r.EffectiveTo = &stamp
	closedAt := lastSequence
	r.ToSequence = &closedAt
	return nil
}

// ApplyRevisionInput 是现场提交的一版变更条件。指针字段用于区分"未提交该
// 字段"与"提交零值"：保持不变的条件可以省略，黏结材料允许显式清空。
type ApplyRevisionInput struct {
	Method         *string
	Adhesive       *string
	TargetExposure *int
	Reason         string
	ChangedBy      string
}

// NewPlanRevision 校验现场变更并在当前有效版本之上追加新版本。
//
// 确定的拒绝结果：
//   - 方案不处于 active：plan_not_revisable（409），完成后同样走到这里；
//   - 任一提交条件本身不合法（如暴露比例越界、方法为空）：invalid（400），
//     即"回退到无效条件"没有任何例外；
//   - 提交后的条件与当前有效版本完全相同：revision_unchanged（409），
//     用于消除重复提交产生的空版本；
//   - 回退到更早的合法条件属于正常变更，仍作为新版本追加，其有效区间照常有界。
func NewPlanRevision(
	id string,
	plan PrepPlan,
	current PlanRevision,
	lastSequence int,
	input ApplyRevisionInput,
	now time.Time,
) (PlanRevision, error) {
	if id == "" {
		return PlanRevision{}, contracts.Internal("missing_generated_id", "方案版本标识未生成", nil)
	}
	if plan.Status != PlanActive {
		return PlanRevision{}, contracts.Conflict("plan_not_revisable", "只有进行中的活动方案可以提交变更")
	}
	if !current.IsCurrent() {
		return PlanRevision{}, contracts.Conflict("revision_not_current", "变更必须基于当前有效版本")
	}

	method := current.Method
	if input.Method != nil {
		value, err := requiredText("修复方法", *input.Method, 160)
		if err != nil {
			return PlanRevision{}, err
		}
		method = value
	}
	adhesive := current.Adhesive
	if input.Adhesive != nil {
		value, err := optionalText("黏结材料", *input.Adhesive, 120)
		if err != nil {
			return PlanRevision{}, err
		}
		adhesive = value
	}
	targetExposure := current.TargetExposure
	if input.TargetExposure != nil {
		if err := validatePercent(*input.TargetExposure); err != nil {
			return PlanRevision{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须位于 0 到 100 之间")
		}
		if *input.TargetExposure == 0 {
			return PlanRevision{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须大于零")
		}
		targetExposure = *input.TargetExposure
	}
	reason, err := requiredText("变更原因", input.Reason, 300)
	if err != nil {
		return PlanRevision{}, err
	}
	changedBy, err := requiredText("变更操作人", input.ChangedBy, 80)
	if err != nil {
		return PlanRevision{}, err
	}

	if method == current.Method &&
		adhesive == current.Adhesive &&
		targetExposure == current.TargetExposure {
		return PlanRevision{}, contracts.Conflict("revision_unchanged", "提交的条件与当前有效版本完全相同，不能重复提交")
	}

	stamp := now.UTC()
	return PlanRevision{
		ID:             id,
		PlanID:         plan.ID,
		Revision:       current.Revision + 1,
		Objective:      current.Objective,
		Method:         method,
		Adhesive:       adhesive,
		TargetExposure: targetExposure,
		Reason:         reason,
		ChangedBy:      changedBy,
		EffectiveFrom:  &stamp,
		FromSequence:   lastSequence + 1,
		CreatedAt:      stamp,
	}, nil
}
