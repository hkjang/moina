package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUpdatePostInputDistinguishesOmittedAndEmptyMediaIDs(t *testing.T) {
	var omitted updatePostInput
	if err := json.Unmarshal([]byte(`{"content":"수정"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.MediaIDs != nil {
		t.Fatalf("omitted mediaIds = %#v, want nil", *omitted.MediaIDs)
	}

	var empty updatePostInput
	if err := json.Unmarshal([]byte(`{"content":"수정","mediaIds":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.MediaIDs == nil || len(*empty.MediaIDs) != 0 {
		t.Fatalf("empty mediaIds = %#v, want non-nil empty slice", empty.MediaIDs)
	}

	var provided updatePostInput
	if err := json.Unmarshal([]byte(`{"content":"수정","mediaIds":["media-new","media-old"]}`), &provided); err != nil {
		t.Fatal(err)
	}
	if provided.MediaIDs == nil || !reflect.DeepEqual(*provided.MediaIDs, []string{"media-new", "media-old"}) {
		t.Fatalf("provided mediaIds = %#v", provided.MediaIDs)
	}
}

func TestResolvePostMediaAltTextsPreservesRetainedAndDefaultsNewMedia(t *testing.T) {
	mediaIDs := []string{"media-new", "media-retained", "media-overridden"}
	supplied := map[string]string{"media-overridden": "  새 문맥 설명  "}
	existing := map[string]string{
		"media-retained":   "기존 문맥 설명",
		"media-overridden": "교체 전 문맥 설명",
	}
	defaults := map[string]string{"media-new": "업로드 기본 설명"}

	resolved, public := resolvePostMediaAltTexts(mediaIDs, supplied, existing, defaults)
	if public != nil {
		t.Fatal(public)
	}
	want := map[string]string{
		"media-new":        "업로드 기본 설명",
		"media-retained":   "기존 문맥 설명",
		"media-overridden": "새 문맥 설명",
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved alt texts = %#v, want %#v", resolved, want)
	}
}

func TestResolvePostMediaAltTextsRejectsKeyOutsideEffectiveAttachments(t *testing.T) {
	_, public := resolvePostMediaAltTexts(
		[]string{"media-attached"},
		map[string]string{"media-not-attached": "잘못된 설명"},
		map[string]string{"media-attached": "기존 설명"},
		nil,
	)
	if public == nil || public.Status != 400 || public.Code != "invalid_media_alt_text" {
		t.Fatalf("public error = %#v", public)
	}
}

func TestMediaReplacementGrandfathersOnlyExistingAttachmentsOverNewLimit(t *testing.T) {
	existing := map[string]string{
		"media-1": "첫째",
		"media-2": "둘째",
		"media-3": "셋째",
	}
	tests := []struct {
		name     string
		mediaIDs []string
		limit    int
		want     bool
	}{
		{name: "within current limit may add", mediaIDs: []string{"media-1", "media-new"}, limit: 2, want: false},
		{name: "retained set above lowered limit", mediaIDs: []string{"media-3", "media-2", "media-1"}, limit: 2, want: false},
		{name: "removing from grandfathered set", mediaIDs: []string{"media-2", "media-1"}, limit: 1, want: false},
		{name: "new media above lowered limit", mediaIDs: []string{"media-1", "media-2", "media-new"}, limit: 2, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mediaReplacementViolatesLimit(test.mediaIDs, test.limit, existing); got != test.want {
				t.Fatalf("mediaReplacementViolatesLimit() = %v, want %v", got, test.want)
			}
		})
	}
}
