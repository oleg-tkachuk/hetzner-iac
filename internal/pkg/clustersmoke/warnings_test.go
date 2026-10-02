package clustersmoke

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An API server warning reaches Logf, which is only printed when progress is
// asked for, rather than client-go's klog line in the middle of the results.
func TestWarnings_GoToLogf(t *testing.T) {
	t.Parallel()

	var got []string

	handler := warnings{logf: func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) }}
	handler.HandleWarningHeader(299, "kube-apiserver", "would violate PodSecurity")

	assert.Equal(t, []string{"api warning: would violate PodSecurity"}, got)
}
