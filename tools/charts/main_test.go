package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppVersionStatus(t *testing.T) {
	t.Parallel()

	// This is what a Renovate pull request runs into. Renovate bumps the chart
	// Version — the helm datasource knows nothing about app versions — so the
	// AppVersion beside it is stale until a human fixes it, and that is the
	// case this must catch.
	for name, tc := range map[string]struct {
		pinned, upstream string
		wantStatus       string
		wantAgrees       bool
	}{
		"equal":                {"3.6.12", "3.6.12", "ok", true},
		"stale after a bump":   {"3.6.12", "3.7.0", "WRONG", false},
		"both absent":          {"", "", "ok", true},
		"claims one, has none": {"1.0.0", "", "WRONG", false},
		"has one, claims none": {"", "1.0.0", "WRONG", false},
		"v prefix must match":  {"1.19.2", "v1.19.2", "WRONG", false},
	} {
		status, agrees := AppVersionStatus(tc.pinned, tc.upstream)

		assert.Equal(t, tc.wantStatus, status, name)
		assert.Equal(t, tc.wantAgrees, agrees, name)
	}
}

func TestDisplay_NamesAnAbsentVersion(t *testing.T) {
	t.Parallel()

	// A blank column in a report reads as a bug in the report.
	assert.Equal(t, "(none)", display(""))
	assert.Equal(t, "1.2.3", display("1.2.3"))
}
