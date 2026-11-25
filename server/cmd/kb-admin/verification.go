package main

import (
	"context"
	"encoding/json"
	"io"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// hostChecks applies the evidence policy as the persisted host owner. There is
// no public HTTP route; kb-admin is the only production entry point.
type hostChecks interface {
	CheckHost(context.Context, string) (domain.Verification, error)
}

// hostLessons publishes a lesson only after an exact previewed confirmation.
type hostLessons interface {
	PreviewLessonHost(context.Context, domain.LessonInput) (service.LessonPreview, error)
	PublishLessonHost(context.Context, service.LessonPublishInput) (domain.Publication, error)
}

func verificationCommand(command string) bool {
	return command == "verify-check" || command == "lesson-publish"
}

// verificationInput holds the strict JSON for verify-check and lesson-publish.
type verificationInput struct {
	check   struct{ Revision string }
	preview domain.LessonInput
	publish service.LessonPublishInput
}

func (in *verificationInput) target(command string, preview bool) any {
	switch {
	case command == "verify-check":
		return &in.check
	case preview:
		return &in.preview
	default:
		return &in.publish
	}
}

func runVerification(ctx context.Context, command string, preview bool, in verificationInput, admin administration, output io.Writer) error {
	var result any
	var err error
	switch {
	case command == "verify-check":
		if admin.checks == nil {
			return domain.ErrUnavailable
		}
		result, err = admin.checks.CheckHost(ctx, in.check.Revision)
	case admin.lessons == nil:
		return domain.ErrUnavailable
	case preview:
		result, err = admin.lessons.PreviewLessonHost(ctx, in.preview)
	default:
		result, err = admin.lessons.PublishLessonHost(ctx, in.publish)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

// unavailableSources stands in when Git is not installed, so commands that do
// not read repository snapshots still compose; Git evidence fails closed.
type unavailableSources struct{}

func (unavailableSources) ReadSnapshot(context.Context, string, string, string) (domain.SourceSnapshot, error) {
	return domain.SourceSnapshot{}, domain.ErrUnavailable
}

func composeSources() (service.SourceReader, error) {
	mapping, err := platform.LoadGitRepositories()
	if err != nil {
		return nil, err
	}
	reader, err := gateway.NewGitSourceReader(mapping)
	switch {
	case err == nil:
		return reader, nil
	case len(mapping) == 0:
		return unavailableSources{}, nil
	default:
		return nil, err
	}
}
