package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// AmendPlan 提交一次方案条件变更。返回的布尔值表示是否新建了版本：
// 重复提交会返回已存在的相同版本而不新建，过期或冲突的基准版本返回 409。
func (s *Service) AmendPlan(
	ctx context.Context,
	planID string,
	request contracts.AmendPlanRequest,
) (domain.PlanRevision, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanRevision{}, false, err
	}
	if request.BaseRevision < 1 {
		return domain.PlanRevision{}, false, contracts.Invalid("invalid_base_revision", "基准版本必须大于零")
	}

	var revision domain.PlanRevision
	created := false
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		if request.BaseRevision != plan.CurrentRevision {
			// 重复提交：内容与已存在的下一版本完全一致时返回该版本，不产生新状态。
			for _, existing := range m.RevisionsForPlan(planID) {
				if existing.Revision == request.BaseRevision+1 && existing.MatchesContent(
					request.Method,
					request.Adhesive,
					request.TargetExposure,
					request.Reason,
					request.ChangedBy,
				) {
					revision = existing
					return nil
				}
			}
			return contracts.Conflict("stale_plan_revision", "方案条件版本已变化，请读取最新版本后重新提交")
		}
		next, err := plan.Amend(
			request.Method,
			request.Adhesive,
			request.TargetExposure,
			request.Reason,
			request.ChangedBy,
			m.ProgressForPlan(planID),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.PutPlan(plan)
		m.AppendRevision(next)
		revision = next
		created = true
		return nil
	})
	return revision, created, err
}
