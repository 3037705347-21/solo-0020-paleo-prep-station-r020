package station

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"

	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

type Service struct {
	repo *repository.MemoryRepository
	now  func() time.Time
	seq  atomic.Uint64
}

func New(repo *repository.MemoryRepository) *Service {
	if repo == nil {
		repo = repository.NewMemory()
	}
	return &Service{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) nextID(prefix string) string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(random[:])
	}
	local := s.seq.Add(1)
	return prefix + "-" + time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString([]byte{byte(local)})
}

// RevisionWindow 在读取时固定一个条件版本的有效范围：
// 从 EffectiveFromSequence 开始，到 EffectiveToSequence 结束；
// EffectiveToSequence 为空表示该版本当前仍然有效。
type RevisionWindow struct {
	domain.PlanRevision
	EffectiveToSequence *int `json:"effective_to_sequence"`
}

type PlanSnapshot struct {
	Plan      domain.PrepPlan        `json:"plan"`
	Revisions []RevisionWindow       `json:"revisions"`
	Progress  []domain.ProgressEntry `json:"progress"`
}

// revisionWindows 固定每个条件版本的有效范围：下一版本的起始序号即上一版本的
// 结束序号；最新版本在方案完成前保持开放。范围可能为空（生效序号大于结束序号），
// 表示该版本生效期间没有写入任何进度。
func revisionWindows(
	revisions []domain.PlanRevision,
	plan domain.PrepPlan,
	progressCount int,
) []RevisionWindow {
	windows := make([]RevisionWindow, 0, len(revisions))
	for index, revision := range revisions {
		var end *int
		if index+1 < len(revisions) {
			closed := revisions[index+1].EffectiveFromSequence - 1
			end = &closed
		} else if plan.Status == domain.PlanCompleted {
			closed := progressCount
			end = &closed
		}
		windows = append(windows, RevisionWindow{PlanRevision: revision, EffectiveToSequence: end})
	}
	return windows
}

func (s *Service) ListSpecimens(ctx context.Context) ([]domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		values = m.ListSpecimens()
		return nil
	})
	return values, err
}

func (s *Service) GetSpecimen(ctx context.Context, specimenID string) (domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return domain.Specimen{}, err
	}
	var value domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		found, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		value = found
		return nil
	})
	return value, err
}
