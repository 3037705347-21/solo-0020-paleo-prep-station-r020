package domain

import (
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// PlanRevision 固定方案条件的一个版本。初始方案为第 1 版，
// 每次变更追加一个版本，历史版本和已盖章的进度都不会被改写。
type PlanRevision struct {
	PlanID         string    `json:"plan_id"`
	Revision       int       `json:"revision"`
	Method         string    `json:"method"`
	Adhesive       string    `json:"adhesive"`
	TargetExposure int       `json:"target_exposure"`
	Reason         string    `json:"reason"`
	ChangedBy      string    `json:"changed_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// EffectiveFromSequence 是该版本开始生效的进度序号，提交变更时固定。
	EffectiveFromSequence int `json:"effective_from_sequence"`
}

func NewInitialRevision(plan PrepPlan, now time.Time) PlanRevision {
	return PlanRevision{
		PlanID:                plan.ID,
		Revision:              1,
		Method:                plan.Method,
		Adhesive:              plan.Adhesive,
		TargetExposure:        plan.TargetExposure,
		Reason:                "初始方案",
		CreatedAt:             now.UTC(),
		EffectiveFromSequence: 1,
	}
}

// MatchesContent 判断一次提交是否与已有版本内容一致，用于识别重复提交。
func (r PlanRevision) MatchesContent(
	method string,
	adhesive string,
	targetExposure int,
	reason string,
	changedBy string,
) bool {
	return r.Method == strings.TrimSpace(method) &&
		r.Adhesive == strings.TrimSpace(adhesive) &&
		r.TargetExposure == targetExposure &&
		r.Reason == strings.TrimSpace(reason) &&
		r.ChangedBy == strings.TrimSpace(changedBy)
}

// Amend 校验并应用一次方案变更，返回追加的新版本。
// previous 是方案已有的进度记录，用于固定生效序号并阻止目标回退到无效条件。
func (p *PrepPlan) Amend(
	method string,
	adhesive string,
	targetExposure int,
	reason string,
	changedBy string,
	previous []ProgressEntry,
	now time.Time,
) (PlanRevision, error) {
	if p.Status != PlanApproved && p.Status != PlanActive {
		return PlanRevision{}, contracts.Conflict("plan_not_amendable", "当前方案状态不能提交方案变更")
	}
	approach, err := requiredText("修复方法", method, 160)
	if err != nil {
		return PlanRevision{}, err
	}
	adhesiveValue, err := optionalText("黏结材料", adhesive, 120)
	if err != nil {
		return PlanRevision{}, err
	}
	if err := validatePercent(targetExposure); err != nil {
		return PlanRevision{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须位于 0 到 100 之间")
	}
	if targetExposure == 0 {
		return PlanRevision{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须大于零")
	}
	reasonValue, err := requiredText("变更原因", reason, 300)
	if err != nil {
		return PlanRevision{}, err
	}
	operator, err := requiredText("变更操作人", changedBy, 80)
	if err != nil {
		return PlanRevision{}, err
	}
	if len(previous) > 0 && targetExposure < previous[len(previous)-1].Percent {
		return PlanRevision{}, contracts.Conflict("target_below_recorded_progress", "目标暴露比例不能低于已记录的进度比例")
	}
	stamp := now.UTC()
	revision := PlanRevision{
		PlanID:                p.ID,
		Revision:              p.CurrentRevision + 1,
		Method:                approach,
		Adhesive:              adhesiveValue,
		TargetExposure:        targetExposure,
		Reason:                reasonValue,
		ChangedBy:             operator,
		CreatedAt:             stamp,
		EffectiveFromSequence: len(previous) + 1,
	}
	p.Method = approach
	p.Adhesive = adhesiveValue
	p.TargetExposure = targetExposure
	p.CurrentRevision = revision.Revision
	p.Version++
	p.UpdatedAt = stamp
	return revision, nil
}
