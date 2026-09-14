package clustersmoke_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clustersmoke"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The bind timeout used throughout. Short, because every case here either
// binds immediately or is meant to time out, and the real default would make
// the failing cases take two minutes each.
const testBindTimeout = 50 * time.Millisecond

func node(name string, ready bool) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: status},
		}},
	}
}

func storageClass(mode storagev1.VolumeBindingMode) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: platform.StorageClass},
		Provisioner:       "csi.hetzner.cloud",
		VolumeBindingMode: &mode,
	}
}

// bindClaimsOn makes the fake clientset behave like a working CSI driver: a
// created claim comes back Bound. Without this every claim stays Pending,
// which is what a broken driver looks like — so the reactor is what lets the
// healthy path be tested at all.
func bindClaimsOn(client *fake.Clientset) {
	client.PrependReactor("create", "persistentvolumeclaims",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			claim, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim)
			if !ok {
				return false, nil, nil
			}

			claim.Status.Phase = corev1.ClaimBound
			claim.Spec.VolumeName = "pvc-probe"

			return false, claim, nil
		})
}

func runner(t *testing.T, objects []runtime.Object, prepare func(*fake.Clientset)) *clustersmoke.Runner {
	t.Helper()

	client := fake.NewSimpleClientset(objects...)
	if prepare != nil {
		prepare(client)
	}

	return clustersmoke.NewWithClient(client, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	})
}

// resultFor finds one check's result by a fragment of the name it reports
// under, so a case names the check it means rather than indexing into the
// report by position.
func resultFor(t *testing.T, report clustersmoke.Report, fragment string) clustersmoke.Result {
	t.Helper()

	for _, result := range report {
		if strings.Contains(result.Name, fragment) {
			return result
		}
	}

	t.Fatalf("no check whose name contains %q, in %d results", fragment, len(report))

	return clustersmoke.Result{}
}

func TestRun_ReportsAllThreeChecksEvenWhenOneFails(t *testing.T) {
	t.Parallel()

	// The three answer independent questions, so a broken CSI must not hide a
	// broken CCM: finding both in one run is one investigation instead of two.
	r := runner(t, []runtime.Object{
		node("cp-0", true),
		storageClass(storagev1.VolumeBindingImmediate),
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "traefik"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
	}, nil) // no bind reactor: the claim stays Pending, as with a broken driver

	report := r.Run(context.Background())

	require.Len(t, report, 3, "a check that returns nothing is a check nobody notices")
	assert.True(t, report.Failed())

	assert.Equal(t, clustersmoke.StatusPassed, resultFor(t, report, "node is Ready").Status)
	assert.Equal(t, clustersmoke.StatusFailed, resultFor(t, report, "reaches Bound").Status)
	assert.Equal(t, clustersmoke.StatusFailed, resultFor(t, report, "has an address").Status)
}

func TestRun_PassesOnAHealthyClusterAndSkipsWhatItCannotJudge(t *testing.T) {
	t.Parallel()

	// The dev cluster's actual shape: three Ready nodes, a late-binding class,
	// and no LoadBalancer Service at all.
	r := runner(t, []runtime.Object{
		node("cp-0", true), node("cp-1", true), node("cp-2", true),
		storageClass(storagev1.VolumeBindingWaitForFirstConsumer),
	}, bindClaimsOn)

	report := r.Run(context.Background())

	assert.False(t, report.Failed())
	assert.Equal(t, 1, report.Skipped())

	storage := resultFor(t, report, "reaches Bound")
	assert.Equal(t, clustersmoke.StatusPassed, storage.Status)
	assert.Contains(t, storage.Detail, "binds on first consumer",
		"the detail must say a pod was scheduled, or the reader cannot tell why one exists")
}

func TestCheckStorage_SchedulesAConsumerOnlyForALateBindingClass(t *testing.T) {
	t.Parallel()

	// hcloud-volumes is WaitForFirstConsumer, so the pod is load-bearing: with
	// no consumer the claim never binds. An Immediate class needs none, and
	// creating one anyway would leave a pod behind on every run.
	for name, tc := range map[string]struct {
		mode     storagev1.VolumeBindingMode
		wantsPod bool
	}{
		"late binding": {storagev1.VolumeBindingWaitForFirstConsumer, true},
		"immediate":    {storagev1.VolumeBindingImmediate, false},
	} {
		client := fake.NewSimpleClientset(node("cp-0", true), storageClass(tc.mode))
		bindClaimsOn(client)

		var podCreated bool

		client.PrependReactor("create", "pods",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				podCreated = true

				return false, nil, nil
			})

		smoke := clustersmoke.NewWithClient(client, clustersmoke.Options{
			StorageClass: platform.StorageClass,
			BindTimeout:  testBindTimeout,
		})

		smoke.Run(context.Background())

		assert.Equal(t, tc.wantsPod, podCreated, name)
	}
}

func TestCheckStorage_DeletesItsProbeEvenWhenTheClaimNeverBinds(t *testing.T) {
	t.Parallel()

	// A Pending claim left behind is a volume the next run has to reason
	// around, and the failing path is exactly when cleanup is forgotten.
	client := fake.NewSimpleClientset(
		node("cp-0", true),
		storageClass(storagev1.VolumeBindingWaitForFirstConsumer),
	)

	var deleted []string

	client.PrependReactor("delete", "*",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			deleted = append(deleted, action.GetResource().Resource)

			return false, nil, nil
		})

	smoke := clustersmoke.NewWithClient(client, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	})

	report := smoke.Run(context.Background())

	require.True(t, report.Failed())
	assert.Contains(t, deleted, "persistentvolumeclaims")
	assert.Contains(t, deleted, "pods")
}

func TestCheckStorage_SaysWhereToLookWhenTheClassIsMissing(t *testing.T) {
	t.Parallel()

	// A claim naming a class that does not exist stays Pending with nothing
	// saying why — the drift pkg/platform.StorageClass exists to prevent. The
	// check has to name the layer that registers it.
	r := runner(t, []runtime.Object{node("cp-0", true)}, nil)

	result := resultFor(t, r.Run(context.Background()), "reaches Bound")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "10-node-platform")
}

func TestCheckLoadBalancers_IgnoresServicesOfEveryOtherType(t *testing.T) {
	t.Parallel()

	// ClusterIP services are the overwhelming majority and never get an
	// external address; counting them would fail every healthy cluster.
	r := runner(t, []runtime.Object{
		node("cp-0", true),
		storageClass(storagev1.VolumeBindingImmediate),
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "hubble-peer", Namespace: "kube-system"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		},
	}, bindClaimsOn)

	result := resultFor(t, r.Run(context.Background()), "has an address")

	assert.Equal(t, clustersmoke.StatusSkipped, result.Status)
}

func TestCheckNodes_TreatsAMissingReadyConditionAsNotReady(t *testing.T) {
	t.Parallel()

	// Absence of the condition is absence of the guarantee. A node that has
	// registered but never reported would otherwise read as healthy.
	r := runner(t, []runtime.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "silent"}},
		storageClass(storagev1.VolumeBindingImmediate),
	}, bindClaimsOn)

	result := resultFor(t, r.Run(context.Background()), "node is Ready")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "silent")
}
