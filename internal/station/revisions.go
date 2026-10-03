package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// RevisePlan 在活动方案上提交一版现场条件变更。
//
// 整个检查与提交运行在仓储的同一个互斥事务内，因此并发提交会被串行化，
// 每一笔都基于前一笔提交后的状态做判断，结果确定：
//   - 方案不存在 → not_found；
//   - 方案不是 active（含 completed/cancelled/draft/approved）→ conflict；
//   - 给出 expected_revision 但不等于当前有效版本 → revision_conflict（并发的
//     后到变更固定失败，先到者成功，不会出现两版都写在同一基版上）；
//   - 提交条件不合法（含"回退到无效条件"）→ invalid，不产生任何版本；
//   - 条件与当前版本完全相同 → revision_unchanged，重复提交不会产生空版本；
//   - 回退到历史上的合法条件 → 作为新版本正常追加，历史版本保持不变。
func (s *Service) RevisePlan(
	ctx context.Context,
	planID string,
	request contracts.RevisionRequest,
) (domain.PlanRevision, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanRevision{}, err
	}

	var created domain.PlanRevision
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		revisions := m.RevisionsForPlan(planID)
		current, err := currentRevision(revisions)
		if err != nil {
			return err
		}
		if request.ExpectedRevision != nil &&
			*request.ExpectedRevision != current.Revision {
			return contracts.Conflict(
				"revision_conflict",
				"方案条件已被其他变更更新，请基于当前有效版本重新提交",
			)
		}
		lastSequence := len(m.ProgressForPlan(planID))
		revision, err := domain.NewPlanRevision(
			s.nextID("revision"),
			plan,
			current,
			lastSequence,
			domain.ApplyRevisionInput{
				Method:         request.Method,
				Adhesive:       request.Adhesive,
				TargetExposure: request.TargetExposure,
				Reason:         request.Reason,
				ChangedBy:      request.ChangedBy,
			},
			s.now(),
		)
		if err != nil {
			return err
		}
		// 先闭合当前版本的有效范围，再追加新版本；两者与方案版本号在同一
		// 事务内提交，任何一步失败都不会留下半开的版本区间。
		if err := current.Close(s.now(), lastSequence); err != nil {
			return err
		}
		if err := plan.MarkRevised(revision, s.now()); err != nil {
			return err
		}
		if err := m.SaveRevision(current); err != nil {
			return err
		}
		if err := m.AppendRevision(revision); err != nil {
			return err
		}
		m.PutPlan(plan)
		created = revision
		return nil
	})
	return created, err
}

// currentRevision 从版本历史中找出唯一的当前有效版本。没有生效版本（草稿
// 尚未审批）或存在多个未闭合版本都属于不变量被破坏。
func currentRevision(revisions []domain.PlanRevision) (domain.PlanRevision, error) {
	var current *domain.PlanRevision
	for i := range revisions {
		if revisions[i].IsCurrent() {
			if current != nil {
				return domain.PlanRevision{}, contracts.Internal(
					"revision_history_invalid",
					"方案存在多个未闭合的有效版本",
					nil,
				)
			}
			current = &revisions[i]
		}
	}
	if current == nil {
		return domain.PlanRevision{}, contracts.Conflict(
			"plan_not_revisable",
			"方案尚无生效的条件版本，不能提交变更",
		)
	}
	return *current, nil
}
