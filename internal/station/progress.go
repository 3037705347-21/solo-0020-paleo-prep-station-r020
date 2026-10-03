package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) LogProgress(
	ctx context.Context,
	planID string,
	request contracts.ProgressRequest,
) (domain.ProgressEntry, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProgressEntry{}, err
	}

	var created domain.ProgressEntry
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		if request.PlanRevision < 0 {
			return contracts.Invalid("invalid_plan_revision", "方案版本必须大于零")
		}
		if request.PlanRevision != 0 && request.PlanRevision != plan.CurrentRevision {
			return contracts.Conflict("stale_plan_revision", "方案条件版本已变化，请依据当前有效版本记录进度")
		}
		previous := m.ProgressForPlan(planID)
		entry, err := domain.NewProgressEntry(
			s.nextID("progress"),
			plan,
			previous,
			request.Stage,
			request.Percent,
			request.Note,
			request.RecordedBy,
			s.now(),
		)
		if err != nil {
			return err
		}
		if plan.Status == domain.PlanApproved {
			if err := plan.Begin(s.now()); err != nil {
				return err
			}
		}
		if request.Percent == 100 {
			if err := plan.Complete(s.now()); err != nil {
				return err
			}
			if err := specimen.MarkStabilized(s.now()); err != nil {
				return err
			}
		}
		m.AppendProgress(entry)
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		created = entry
		return nil
	})
	return created, err
}
