package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// NoticeRepository reads the dreaming outbox and records delivery outcomes.
type NoticeRepository interface {
	Notice(context.Context, Tx, string) (domain.Notice, error)
	Findings(context.Context, Tx, []string) ([]domain.Finding, error)
	Revisions(context.Context, Tx, []string) ([]DreamInput, error)
	MarkNotice(context.Context, Tx, string, domain.NoticeState, []string, []string) error
}

// NoticeSender is the consumer port for owner delivery (the Buzz gateway). The
// operation key is stable per notice so a retry after a lost acknowledgment
// can be deduplicated by the gateway.
type NoticeSender interface {
	Send(ctx context.Context, operationKey, threadID, body string) error
}

type NotificationJobs interface {
	Commit(context.Context, domain.Lease, string, func(context.Context, Tx) error) error
}

type NotificationService struct {
	tx       Transactor
	repo     NoticeRepository
	jobs     NotificationJobs
	sender   NoticeSender
	threadID string
}

func NewNotificationService(tx Transactor, repo NoticeRepository, jobs NotificationJobs, sender NoticeSender, threadID string) *NotificationService {
	return &NotificationService{tx, repo, jobs, sender, threadID}
}

const (
	maxNoticeFindingBytes = 280
	maxNoticeBytes        = 4000
)

// Dispatch delivers one outbox notice. It rechecks every finding's relevance
// and current support immediately before sending; sensitive, stale or already
// delivered findings never leave. The send happens outside any transaction.
func (s *NotificationService) Dispatch(ctx context.Context, job domain.Job, lease domain.Lease) error {
	if job.Kind != NotifyJobKind || domain.ValidateIDs(job.InputDigest) != nil {
		return domain.ErrInvalidInput
	}
	var notice domain.Notice
	var deliver []domain.Finding
	var suppress []string
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if notice, err = s.repo.Notice(ctx, tx, job.InputDigest); err != nil {
			return err
		}
		if notice.State != domain.NoticePending {
			return nil
		}
		findings, err := s.repo.Findings(ctx, tx, notice.FindingIDs)
		if err != nil {
			return err
		}
		for _, f := range findings {
			relevant, err := s.relevant(ctx, tx, f)
			if err != nil {
				return err
			}
			if relevant {
				deliver = append(deliver, f)
			} else {
				suppress = append(suppress, f.ID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if notice.State != domain.NoticePending {
		return s.jobs.Commit(ctx, lease, job.InputDigest, func(context.Context, Tx) error { return nil })
	}
	state := domain.NoticeSuppressed
	delivered := make([]string, 0, len(deliver))
	if len(deliver) > 0 {
		if s.sender == nil || s.threadID == "" {
			return domain.ErrUnavailable
		}
		if err := s.sender.Send(ctx, notice.ID, s.threadID, renderNotice(notice, deliver)); err != nil {
			return err
		}
		state = domain.NoticeSent
		for _, f := range deliver {
			delivered = append(delivered, f.ID)
		}
	}
	return s.jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx Tx) error {
		return s.repo.MarkNotice(ctx, tx, notice.ID, state, delivered, suppress)
	})
}

func (s *NotificationService) relevant(ctx context.Context, tx Tx, f domain.Finding) (bool, error) {
	if f.Sensitive || f.Status != domain.FindingPending || len(f.SourceRevisions) == 0 {
		return false, nil
	}
	current, err := s.repo.Revisions(ctx, tx, f.SourceRevisions)
	if err != nil {
		return false, err
	}
	if len(current) != len(f.SourceRevisions) {
		return false, nil
	}
	for _, c := range current {
		if !c.Current {
			return false, nil
		}
	}
	return true, nil
}

func renderNotice(n domain.Notice, findings []domain.Finding) string {
	var b strings.Builder
	if n.Urgency == domain.FindingImmediate {
		b.WriteString("KB finding needs attention")
	} else {
		b.WriteString("KB daily findings digest")
	}
	b.WriteString(" (project " + n.ProjectID + ", candidates only, not verified)\n")
	for _, f := range findings {
		line := "- [" + f.Kind + "] " + truncateUTF8(strings.Join(strings.Fields(f.Text), " "), maxNoticeFindingBytes) + " (finding " + f.ID + ")\n"
		if b.Len()+len(line) > maxNoticeBytes {
			b.WriteString("- more findings available on demand\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
