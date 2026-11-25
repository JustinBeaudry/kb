package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHostChecks struct {
	revision                            string
	previewed                           domain.LessonInput
	published                           service.LessonPublishInput
	checkCalls, previewCalls, publishes int
}

func (f *fakeHostChecks) CheckHost(_ context.Context, revision string) (domain.Verification, error) {
	f.revision = revision
	f.checkCalls++
	return domain.Verification{ID: "verification", Outcome: domain.VerificationSupported}, nil
}
func (f *fakeHostChecks) PreviewLessonHost(_ context.Context, in domain.LessonInput) (service.LessonPreview, error) {
	f.previewed = in
	f.previewCalls++
	return service.LessonPreview{Digest: "lesson-digest", State: domain.PublicationPublished}, nil
}
func (f *fakeHostChecks) PublishLessonHost(_ context.Context, in service.LessonPublishInput) (domain.Publication, error) {
	f.published = in
	f.publishes++
	return domain.Publication{ID: "publication", State: domain.PublicationPublished}, nil
}

func checksFactory(t *testing.T, f *fakeHostChecks) adminFactory {
	return func(_ context.Context, objects bool) (administration, func(), error) {
		require.True(t, objects)
		return administration{checks: f, lessons: f, recovery: &fakeHostRecovery{}}, func() {}, nil
	}
}

func TestHostVerifyCheckDispatchesRevision(t *testing.T) {
	f := &fakeHostChecks{}
	var out bytes.Buffer

	err := runAdmin(t.Context(), []string{"verify-check"}, &out, strings.NewReader(`{"Revision":"candidate"}`), checksFactory(t, f))

	require.NoError(t, err)
	assert.Equal(t, "candidate", f.revision)
	assert.Contains(t, out.String(), `"Outcome":"supported"`)
}

func TestHostLessonPublishRequiresPreviewedDigest(t *testing.T) {
	const lesson = `"ProjectID":"demo","LessonID":"lesson","ExpectedHead":"","Text":"Retry once in CI.","SharingScope":"project","Applicability":{"Environment":"ci"},"SupportRevisions":["s1"]`
	f := &fakeHostChecks{}
	var out bytes.Buffer
	require.NoError(t, runAdmin(t.Context(), []string{"lesson-publish", "--preview"}, &out, strings.NewReader(`{`+lesson+`}`), checksFactory(t, f)))
	assert.Equal(t, 1, f.previewCalls)
	assert.Zero(t, f.publishes, "preview never publishes")
	assert.Equal(t, "lesson", f.previewed.LessonID)
	assert.Contains(t, out.String(), `"Digest":"lesson-digest"`)
	out.Reset()

	err := runAdmin(t.Context(), []string{"lesson-publish"}, &out, strings.NewReader(`{`+lesson+`,"ExpectedDigest":"lesson-digest"}`), checksFactory(t, f))

	require.NoError(t, err)
	assert.Equal(t, 1, f.publishes)
	assert.Equal(t, "lesson-digest", f.published.ExpectedDigest)
	assert.Equal(t, []string{"s1"}, f.published.SupportRevisions)
	assert.Contains(t, out.String(), `"State":"published"`)
}

func TestHostCheckCommandsRejectMalformedInputBeforeComposition(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		input string
	}{
		{"check has no preview", []string{"verify-check", "--preview"}, `{"Revision":"r"}`},
		{"check unknown field", []string{"verify-check"}, `{"Revision":"r","Principal":"forged"}`},
		{"preview carries no digest", []string{"lesson-publish", "--preview"}, `{"LessonID":"l","ExpectedDigest":"d"}`},
		{"publish unknown field", []string{"lesson-publish"}, `{"LessonID":"l","Bearer":"secret"}`},
		{"trailing json", []string{"lesson-publish"}, `{} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false

			err := runAdmin(t.Context(), tc.args, io.Discard, strings.NewReader(tc.input), func(context.Context, bool) (administration, func(), error) {
				called = true
				return administration{}, func() {}, nil
			})

			assert.ErrorIs(t, err, domain.ErrInvalidInput)
			assert.False(t, called)
		})
	}
}

func TestHostCheckCommandsRequireJournalRecovery(t *testing.T) {
	for _, args := range [][]string{{"verify-check"}, {"lesson-publish"}, {"lesson-publish", "--preview"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			recovery := &fakeHostRecovery{err: domain.ErrUnavailable}
			f := &fakeHostChecks{}

			err := runAdmin(t.Context(), args, io.Discard, strings.NewReader(`{}`), func(context.Context, bool) (administration, func(), error) {
				return administration{checks: f, lessons: f, recovery: recovery}, func() {}, nil
			})

			assert.ErrorIs(t, err, domain.ErrUnavailable)
			assert.Equal(t, 1, recovery.calls)
			assert.Zero(t, f.checkCalls+f.previewCalls+f.publishes)
		})
	}
}

func TestHelpDescribesHostVerificationCommands(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"help"}, &out))
	for _, want := range []string{"verify-check", "lesson-publish --preview", "KB_GIT_REPOSITORIES"} {
		assert.Contains(t, out.String(), want)
	}
}
