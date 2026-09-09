package hetzner_test

import (
	"fmt"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
)

func TestProbe(t *testing.T) {
	lower := `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata: { name: c }
placement: { location: fsn1 }
network: { adminCIDRs: [203.0.113.4/32] }
talos: { version: v1.14.0 }
workerpools:
  - name: worker
    count: 2
    serverType: cx32
`
	top, err := hetzner.ParseTopology([]byte(lower))
	fmt.Printf("lowercase key -> err=%v pools=%+v\n", err, top.WorkerPools)

	bogus := `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata: { name: c }
placement: { location: fsn1 }
network: { adminCIDRs: [203.0.113.4/32] }
talos: { version: v1.14.0 }
totallyUnknownKey: 5
`
	_, err2 := hetzner.ParseTopology([]byte(bogus))
	fmt.Printf("unknown key   -> err=%v\n", err2)
}
