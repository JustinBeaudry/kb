package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
)

type HostOwnerRepository interface {
	Owner(context.Context, Tx) (OwnerIdentity, error)
}

// HostCaptureService is composed only into kb-admin. It is never a handler port.
type HostCaptureService struct {
	capture *CaptureService
	owners  HostOwnerRepository
}

func NewHostCaptureService(tx Transactor, r EvidenceRepository, j CaptureJobs, o ObjectStore, owners HostOwnerRepository) *HostCaptureService {
	return &HostCaptureService{NewCaptureService(tx, r, j, o), owners}
}
func (s *HostCaptureService) owner(ctx context.Context, tx Tx) (string, error) {
	p, err := hostOwner(ctx, tx, s.owners)
	if err != nil {
		return "", err
	}
	return ownerActorPrefix + TokenDigest(p.OwnerID), nil
}
func (s *HostCaptureService) StageHost(ctx context.Context, m ArtifactInput, body io.Reader) (domain.Artifact, error) {
	var actor string
	err := s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error { var err error; actor, err = s.owner(ctx, tx); return err })
	if err != nil {
		return domain.Artifact{}, err
	}
	if m.Scope.TaskID != "" || m.Scope.SessionID != "" {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	return s.capture.stage(ctx, actor, m, body, func(ctx context.Context, tx Tx) error {
		current, err := s.owner(ctx, tx)
		if err != nil {
			return err
		}
		if current != actor {
			return domain.ErrForbidden
		}
		return nil
	})
}
func (s *HostCaptureService) load(ctx context.Context, id string, owned bool) (domain.Artifact, error) {
	var a domain.Artifact
	err := s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		actor, err := s.owner(ctx, tx)
		if err != nil {
			return err
		}
		var uploader string
		a, uploader, err = s.capture.repo.Artifact(ctx, tx, id)
		if err != nil {
			return err
		}
		if owned && uploader != actor {
			return domain.ErrForbidden
		}
		if a.State == domain.ArtifactDeleted {
			return domain.ErrDeleted
		}
		if a.State != domain.ArtifactStaged && a.State != domain.ArtifactReady {
			return domain.ErrIncomplete
		}
		return s.capture.policy(ctx, tx, a)
	})
	return a, err
}
func (s *HostCaptureService) CompleteHost(ctx context.Context, id, digest string) (domain.Receipt, error) {
	if !validDigest(digest) {
		return domain.Receipt{}, domain.ErrInvalidInput
	}
	a, err := s.load(ctx, id, true)
	if err != nil {
		return domain.Receipt{}, err
	}
	if a.Digest != digest {
		return domain.Receipt{}, domain.ErrConflict
	}
	if err = s.verifyObject(ctx, a); err != nil {
		return domain.Receipt{}, err
	}
	var receipt domain.Receipt
	err = s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		actor, err := s.owner(ctx, tx)
		if err != nil {
			return err
		}
		_, uploader, err := s.capture.repo.Artifact(ctx, tx, id)
		if err != nil {
			return err
		}
		if uploader != actor {
			return domain.ErrForbidden
		}
		receipt, err = s.capture.publish(ctx, tx, a)
		return err
	})
	if err != nil {
		return domain.Receipt{}, err
	}
	return receipt, nil
}
func (s *HostCaptureService) ReferenceHost(ctx context.Context, ref domain.EvidenceRef, scope domain.Scope) (domain.EvidenceRef, error) {
	a, err := s.load(ctx, ref.ArtifactID, false)
	if err != nil {
		return domain.EvidenceRef{}, err
	}
	if a.Scope != scope || a.Digest != ref.Digest || a.SourceLocator != ref.Locator {
		return domain.EvidenceRef{}, domain.ErrConflict
	}
	if a.State != domain.ArtifactReady || !a.ObjectAvailable {
		return domain.EvidenceRef{}, domain.ErrIncomplete
	}
	if err = s.verifyObject(ctx, a); err != nil {
		return domain.EvidenceRef{}, err
	}
	err = s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.owner(ctx, tx); err != nil {
			return err
		}
		current, actor, err := s.capture.repo.Artifact(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.Generation != a.Generation || current.ObjectKey != a.ObjectKey {
			return domain.ErrStale
		}
		if current.State != domain.ArtifactReady || !current.ObjectAvailable {
			return domain.ErrIncomplete
		}
		ref.Origin = domain.Origin{ClientID: actor, Trust: domain.TrustSubmitted}
		return s.capture.policy(ctx, tx, current)
	})
	if err != nil {
		return domain.EvidenceRef{}, err
	}
	return ref, nil
}

// ReadHost reads the exact source generation outside graph transactions. Unknown
// provider observations preserve health; only confirmed absence/corruption fences it.
func (s *HostCaptureService) ReadHost(ctx context.Context, id string) (domain.Artifact, []byte, error) {
	a, err := s.load(ctx, id, false)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	if a.State != domain.ArtifactReady || !a.ObjectAvailable {
		return domain.Artifact{}, nil, domain.ErrIncomplete
	}
	checkCtx, cancel := captureObjectContext(ctx)
	defer cancel()
	reader, err := s.capture.objects.Open(checkCtx, a.ObjectKey)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(reader, MaxArtifactBytes+1))
		closeErr := reader.Close()
		if err != nil {
			err = domain.ErrUnavailable
		} else if int64(len(data)) != a.Bytes {
			err = domain.ErrIncomplete
		} else {
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != a.Digest {
				err = domain.ErrIncomplete
			} else if closeErr != nil {
				err = domain.ErrUnavailable
			}
		}
	}
	if err != nil {
		if errors.Is(err, domain.ErrIncomplete) {
			healthErr := s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
				if _, err := s.owner(ctx, tx); err != nil {
					return err
				}
				return s.capture.repo.Availability(ctx, tx, a.ID, a.ObjectKey, false)
			})
			if healthErr != nil {
				return domain.Artifact{}, nil, errors.Join(err, healthErr)
			}
		}
		return domain.Artifact{}, nil, err
	}
	err = s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.owner(ctx, tx); err != nil {
			return err
		}
		current, _, err := s.capture.repo.Artifact(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.Generation != a.Generation || current.ObjectKey != a.ObjectKey {
			return domain.ErrStale
		}
		if current.State != domain.ArtifactReady || !current.ObjectAvailable {
			return domain.ErrIncomplete
		}
		return s.capture.policy(ctx, tx, current)
	})
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	return a, data, nil
}

func (s *HostCaptureService) verifyObject(ctx context.Context, a domain.Artifact) error {
	checkCtx, cancel := captureObjectContext(ctx)
	err := s.capture.checkObject(checkCtx, a)
	cancel()
	if err == nil || !errors.Is(err, domain.ErrIncomplete) {
		return err
	}
	healthErr := s.capture.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.owner(ctx, tx); err != nil {
			return err
		}
		return s.capture.repo.Availability(ctx, tx, a.ID, a.ObjectKey, false)
	})
	if healthErr != nil && !errors.Is(healthErr, domain.ErrStale) {
		return errors.Join(err, healthErr)
	}
	return err
}
