package stackstatus_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

func TestSameStack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		listed string
		want   bool
	}{
		// A self-managed backend lists the short name.
		{listed: "dev", want: true},
		// Pulumi Cloud lists it qualified by the organisation.
		{listed: "acme/dev", want: true},
		{listed: "acme/project/dev", want: true},
		// A name that merely ends in the same letters is another stack.
		{listed: "predev", want: false},
		{listed: "acme/predev", want: false},
		{listed: "dev/prod", want: false},
		{listed: "", want: false},
	}

	for _, test := range tests {
		assert.Equal(t, test.want, stackstatus.SameStack(test.listed, "dev"), "%q", test.listed)
	}
}
