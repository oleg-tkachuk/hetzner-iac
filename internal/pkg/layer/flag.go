package layer

import "fmt"

// Flag reads a boolean switch from this layer's stack configuration.
//
// Absent is false. A value that is not a boolean is an ERROR, and the parsing
// itself belongs to the SDK: config.Config.TryBool is Pulumi's own strict
// accessor, and this does not reimplement it.
//
// GetBool is the one to avoid. It reads the same value through the same cast
// and DISCARDS the failure, so `acmeStaging: yes` — YAML's own word for true,
// which neither spf13/cast nor strconv.ParseBool accepts — reads as false. The
// two states do not look different from outside: a cluster that was never
// asked for the feature and a cluster whose request was dropped both lack it,
// and the second one has an operator who believes otherwise.
//
// The direction of that silence decides what it costs. For acmeStaging it is
// the expensive direction — a dropped request sends the first order to Let's
// Encrypt's PRODUCTION endpoint, whose duplicate-certificate limit is per week
// and is not refunded by correcting the config afterwards.
//
// What is left here is the two things TryBool alone does not give. It reports
// an ABSENT key as a failure, which for a switch is the ordinary state and not
// one; and its message names neither the value nor what to do about it, which
// are the only two facts the operator needs.
//
// It was a hand-written parser in two layers before it was here, error message
// included. That is the drift internal/pkg/platform's own doc comment
// describes: a rule spelled in two places is a rule that will eventually be in
// two versions.
func (r *Runner) Flag(key string) (bool, error) {
	raw := r.Cfg.Get(key)
	if raw == "" {
		return false, nil
	}

	value, err := r.Cfg.TryBool(key)
	if err != nil {
		return false, fmt.Errorf(
			"config %q is %q, which is not a boolean: set it to true or false", key, raw)
	}

	return value, nil
}
