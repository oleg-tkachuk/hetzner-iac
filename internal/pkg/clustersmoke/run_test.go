package clustersmoke_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clustersmoke"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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
	// Delete, because that is what the real default class reclaims — the
	// render check holds the chart to it. A fixture leaving it unset would let
	// the data-volume check read an unknown policy and pass.
	reclaim := corev1.PersistentVolumeReclaimDelete

	// The default, as the real one is: a claim naming no class resolves to it
	// through this annotation, not through its name.
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        platform.StorageClass,
			Annotations: map[string]string{clustersmoke.DefaultClassAnnotation: "true"},
		},
		Provisioner:       "csi.hetzner.cloud",
		VolumeBindingMode: &mode,
		ReclaimPolicy:     &reclaim,
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

	return clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
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

func TestRun_ReportsEveryCheckEvenWhenOneFails(t *testing.T) {
	t.Parallel()

	// They answer independent questions, so a broken CSI must not hide a
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

	require.Len(t, report, 9, "a check that returns nothing is a check nobody notices")
	assert.True(t, report.Failed())

	assert.Equal(t, clustersmoke.StatusPassed, resultFor(t, report, "node is Ready").Status)
	assert.Equal(t, clustersmoke.StatusFailed, resultFor(t, report, "reaches Bound").Status)
	assert.Equal(t, clustersmoke.StatusFailed, resultFor(t, report, "has an address").Status)
}

func TestRun_PassesOnAHealthyClusterAndSkipsWhatItCannotJudge(t *testing.T) {
	t.Parallel()

	// The dev cluster's actual shape: three Ready nodes, cluster DNS on two of
	// them, a late-binding storage class, and no LoadBalancer Service at all.
	r := runner(t, threeNodeCluster(), proberExits(0))

	report := r.Run(context.Background())

	assert.False(t, report.Failed())
	// Six skips: the load balancer check, which has nothing to look at, the
	// external metrics check, because this fixture serves no aggregated group,
	// the data-volume check, because no namespace claims to hold data, the
	// two dynamic checks — secret stores and network policies — because this
	// runner has no dynamic client, and the image admission check, because no
	// namespace is labelled. The cross-node check must NOT be skipping here: a
	// cluster of three nodes with DNS on two is exactly where it runs.
	assert.Equal(t, 6, report.Count(clustersmoke.StatusSkipped))
	assert.Equal(t, clustersmoke.StatusPassed,
		resultFor(t, report, "another node").Status)

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

		smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
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

	smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
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
	// saying why — the drift internal/pkg/platform.StorageClass exists to prevent. The
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

// dnsPod is a cluster-DNS pod on a node, as the cross-node plan looks for it.
func dnsPod(name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kube-system",
			Labels:    map[string]string{"k8s-app": "kube-dns"},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{PodIP: "10.244.0." + strconv.Itoa(int(name[len(name)-1]))},
	}
}

// proberExits makes the fake clientset answer as a prober whose every
// container terminated with the given code, the only thing the check reads.
func proberExits(code int32) func(*fake.Clientset) {
	return proberAnswers(func(int) int32 { return code })
}

// proberAnswers makes container i of the prober terminate with code(i).
func proberAnswers(code func(i int) int32) func(*fake.Clientset) {
	return func(client *fake.Clientset) {
		bindClaimsOn(client)

		client.PrependReactor("create", "pods",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				pod, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
				if !ok || pod.Name != "cluster-smoke-crossnode" {
					return false, nil, nil
				}

				pod.Status.Phase = corev1.PodSucceeded
				for i, container := range pod.Spec.Containers {
					pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
						Name: container.Name,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{ExitCode: code(i)},
						},
					})
				}

				return false, pod, nil
			})
	}
}

// threeNodeCluster is the dev cluster's shape: three Ready nodes and cluster
// DNS on two of them.
func threeNodeCluster() []runtime.Object {
	return []runtime.Object{
		node("cp-0", true), node("cp-1", true), node("cp-2", true),
		storageClass(storagev1.VolumeBindingWaitForFirstConsumer),
		dnsPod("coredns-a", "cp-0"),
		dnsPod("coredns-b", "cp-1"),
	}
}

func TestCheckCrossNode_FailsWhenThePathIsBroken(t *testing.T) {
	t.Parallel()

	// THE CHECK THIS PACKAGE GAINED A FOURTH ENTRY FOR. On the real cluster
	// this failure looked like nothing: every node Ready, every pod Running,
	// and pod-to-pod across nodes with no route at all. The prober's non-zero
	// exit is what turns that into one line.
	r := runner(t, threeNodeCluster(), proberExits(1))

	result := resultFor(t, r.Run(context.Background()), "another node")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "cilium-health status")
}

func TestCheckCrossNode_AsksEveryReplicaAndNamesTheOneThatIsSilent(t *testing.T) {
	t.Parallel()

	// One replica refusing pods while the other answers: through the Service
	// that passed most of the time. Measured on dev after a Cilium agent
	// restart left one CoreDNS endpoint's policy map without its DNS allows.
	var commands [][]string

	client := fake.NewSimpleClientset(threeNodeCluster()...)
	proberAnswers(func(i int) int32 { return int32(i) })(client)

	client.PrependReactor("create", "pods",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if pod, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod); ok &&
				pod.Name == "cluster-smoke-crossnode" {
				for _, container := range pod.Spec.Containers {
					commands = append(commands, container.Command)
				}
			}

			return false, nil, nil
		})

	smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	})

	result := resultFor(t, smoke.Run(context.Background()), "another node")

	require.Len(t, commands, 2, "one container per DNS replica")

	for _, command := range commands {
		assert.Len(t, command, 3, "each asks a replica by address, not the Service: %v", command)
	}

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "1 of 2")
	assert.Contains(t, result.Detail, "coredns-b on cp-1")
	assert.NotContains(t, result.Detail, "coredns-a on")
}

func TestCheckCrossNode_PassesAndProbesFromTheDNSFreeNode(t *testing.T) {
	t.Parallel()

	var probedOn string

	client := fake.NewSimpleClientset(threeNodeCluster()...)
	proberExits(0)(client)

	client.PrependReactor("create", "pods",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if pod, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod); ok &&
				pod.Name == "cluster-smoke-crossnode" {
				probedOn = pod.Spec.NodeName
			}

			return false, nil, nil
		})

	smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	})

	result := resultFor(t, smoke.Run(context.Background()), "another node")

	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	// cp-2 is the only Ready node without a DNS replica. Anywhere else and the
	// query could be answered locally, proving nothing about crossing a node.
	assert.Equal(t, "cp-2", probedOn)
}

func TestCheckCrossNode_SkipsOnASingleNodeCluster(t *testing.T) {
	t.Parallel()

	// Skipped, not passed — and this is the case that let the real failure
	// survive: one node has no cross-node path to be broken.
	r := runner(t, []runtime.Object{
		node("cp-0", true),
		storageClass(storagev1.VolumeBindingImmediate),
		dnsPod("coredns-a", "cp-0"),
	}, proberExits(0))

	result := resultFor(t, r.Run(context.Background()), "another node")

	assert.Equal(t, clustersmoke.StatusSkipped, result.Status)
	assert.Contains(t, result.Detail, "no cross-node path")
}

func TestCheckCrossNode_DeletesItsProberEitherWay(t *testing.T) {
	t.Parallel()

	// The failing path is where cleanup is forgotten, and a leftover prober on
	// a pinned node is one the next run has to reason around.
	client := fake.NewSimpleClientset(threeNodeCluster()...)
	proberExits(1)(client)

	var deleted []string

	client.PrependReactor("delete", "pods",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if d, ok := action.(k8stesting.DeleteAction); ok {
				deleted = append(deleted, d.GetName())
			}

			return false, nil, nil
		})

	smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	})

	require.True(t, smoke.Run(context.Background()).Failed())
	assert.Contains(t, deleted, "cluster-smoke-crossnode")
}

func TestProberPod_SatisfiesTheRestrictedPodSecurityStandard(t *testing.T) {
	t.Parallel()

	// A probe that has to be exempted from the cluster's own baseline stops
	// working the day that baseline is enforced, and its warning trains the
	// reader to ignore this output. The storage probe learned this from a
	// four-clause warning on its first real run.
	var created *corev1.Pod

	client := fake.NewSimpleClientset(threeNodeCluster()...)
	proberExits(0)(client)
	client.PrependReactor("create", "pods",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if pod, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod); ok &&
				pod.Name == "cluster-smoke-crossnode" {
				created = pod
			}

			return false, nil, nil
		})

	clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
		StorageClass: platform.StorageClass,
		BindTimeout:  testBindTimeout,
	}).Run(context.Background())

	require.NotNil(t, created, "no prober was created")
	require.NotNil(t, created.Spec.SecurityContext)
	assert.Equal(t, true, *created.Spec.SecurityContext.RunAsNonRoot)
	require.NotNil(t, created.Spec.SecurityContext.RunAsUser,
		"runAsNonRoot needs a numeric user: busybox declares none and the kubelet will not guess")
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault,
		created.Spec.SecurityContext.SeccompProfile.Type)

	require.NotEmpty(t, created.Spec.Containers)

	for _, container := range created.Spec.Containers {
		security := container.SecurityContext
		require.NotNil(t, security, container.Name)
		assert.Equal(t, false, *security.AllowPrivilegeEscalation, container.Name)
		assert.Equal(t, []corev1.Capability{"ALL"}, security.Capabilities.Drop, container.Name)
	}
}

// TestCheckDataVolumes_ReadsTheDefaultClassForAClaimThatNamesNone is the case
// the check exists for, and the one a fixture is easy to get wrong.
//
// A chart that omits storageClassName does not get "no class": Kubernetes binds
// the claim to whichever class carries the default annotation, which here is
// the one that deletes. If this resolved to an empty class name the check would
// find an unknown reclaim policy and pass.
func TestCheckDataVolumes_ReadsTheDefaultClassForAClaimThatNamesNone(t *testing.T) {
	t.Parallel()

	r := runner(t, []runtime.Object{
		node("cp-0", true),
		storageClass(storagev1.VolumeBindingImmediate),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "postgres",
			Labels: map[string]string{platform.DataNamespaceLabel: "true"},
		}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: "data-pg-0", Namespace: "postgres",
		}},
	}, nil)

	result := resultFor(t, r.Run(context.Background()), "holding data")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status,
		"a claim naming no class is on the default one, which reclaims Delete")
	assert.Contains(t, result.Detail, "postgres/data-pg-0")
}

// storeObject is a ClusterSecretStore as the API server returns it: no typed
// struct anywhere, because the check reads it through the dynamic client on
// purpose.
func storeObject(name, status, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "ClusterSecretStore",
		"metadata":   map[string]any{"name": name},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": status, "reason": reason},
			},
		},
	}}
}

// TestCheckSecretStores_ReadsTheReadyConditionOffAnUnstructuredObject is the
// half a pure function cannot cover: digging one condition out of a status
// nobody has a Go type for.
func TestCheckSecretStores_ReadsTheReadyConditionOffAnUnstructuredObject(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	custom := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		// Both resources the dynamic checks list. The fake client panics on a
		// LIST of a resource whose list kind it was not told about, so a new
		// check needs its kind here — which is the shape of this failure the
		// first time.
		map[schema.GroupVersionResource]string{
			clustersmoke.SecretStoreResource:   "ClusterSecretStoreList",
			clustersmoke.NetworkPolicyResource: "CiliumClusterwideNetworkPolicyList",
		},
		storeObject("pulumi-esc", "False", "InvalidProviderConfig"),
	)

	smoke := clustersmoke.NewWithClient(fake.NewSimpleClientset(), custom, clustersmoke.Options{
		StorageClass: platform.StorageClass,
	})

	result := resultFor(t, smoke.Run(context.Background()), "secret store")

	require.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "InvalidProviderConfig")
}

// TestCheckSecretStores_PassesOnAReadyStore is the other side, and it also
// proves the condition's status is read as a STRING: "True" is what the API
// returns, not a boolean.
func TestCheckSecretStores_PassesOnAReadyStore(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	custom := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		// Both resources the dynamic checks list. The fake client panics on a
		// LIST of a resource whose list kind it was not told about, so a new
		// check needs its kind here — which is the shape of this failure the
		// first time.
		map[schema.GroupVersionResource]string{
			clustersmoke.SecretStoreResource:   "ClusterSecretStoreList",
			clustersmoke.NetworkPolicyResource: "CiliumClusterwideNetworkPolicyList",
		},
		storeObject("pulumi-esc", "True", ""),
	)

	smoke := clustersmoke.NewWithClient(fake.NewSimpleClientset(), custom, clustersmoke.Options{
		StorageClass: platform.StorageClass,
	})

	result := resultFor(t, smoke.Run(context.Background()), "secret store")

	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
}

// dataClaimsRunner is a cluster with the platform's two classes — the default
// one, which deletes, and the database one, which retains — one labelled data
// namespace, and the given claims and volumes in it. The probe's own class is
// the retaining one, which is the flag that used to stand in for "the default".
func dataClaimsRunner(t *testing.T, objects ...runtime.Object) *clustersmoke.Runner {
	t.Helper()

	deletes := corev1.PersistentVolumeReclaimDelete
	retains := corev1.PersistentVolumeReclaimRetain

	cluster := []runtime.Object{
		node("cp-0", true),
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        platform.StorageClass,
				Annotations: map[string]string{clustersmoke.DefaultClassAnnotation: "true"},
			},
			Provisioner:   "csi.hetzner.cloud",
			ReclaimPolicy: &deletes,
		},
		&storagev1.StorageClass{
			ObjectMeta:    metav1.ObjectMeta{Name: platform.StorageClassDatabase},
			Provisioner:   "csi.hetzner.cloud",
			ReclaimPolicy: &retains,
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "postgres",
			Labels: map[string]string{platform.DataNamespaceLabel: "true"},
		}},
	}

	return clustersmoke.NewWithClient(fake.NewSimpleClientset(append(cluster, objects...)...), nil, clustersmoke.Options{
		StorageClass: platform.StorageClassDatabase,
		BindTimeout:  testBindTimeout,
	})
}

func dataClaim(name string, class *string, volume string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "postgres"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: class, VolumeName: volume},
	}
}

// TestCheckDataVolumes_ResolvesTheDefaultClassByItsAnnotation is the bug: a
// claim naming no class was judged against --storage-class, the PROBE's
// class. Run with the database class, every such claim passed while it sat on
// the class that deletes.
func TestCheckDataVolumes_ResolvesTheDefaultClassByItsAnnotation(t *testing.T) {
	t.Parallel()

	result := resultFor(t, dataClaimsRunner(t, dataClaim("data-pg-0", nil, "")).Run(context.Background()), "holding data")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status,
		"a claim naming no class is on the annotated default, which deletes, whatever --storage-class says")
	assert.Contains(t, result.Detail, "postgres/data-pg-0")
}

// TestCheckDataVolumes_FailsAClassItCannotFind: a misspelt class used to have
// no reclaim policy, and no policy is not Delete, so it passed.
func TestCheckDataVolumes_FailsAClassItCannotFind(t *testing.T) {
	t.Parallel()

	typo := platform.StorageClassDatabase + "-typo"

	result := resultFor(t, dataClaimsRunner(t, dataClaim("data-pg-0", &typo, "")).Run(context.Background()), "holding data")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status,
		"a claim on a class that does not exist is not known to retain anything")
	assert.Contains(t, result.Detail, typo)
}

// TestCheckDataVolumes_ReadsTheBoundVolumeRatherThanTheClass: what deleting a
// bound claim does is the volume's own reclaim policy, which the class only
// set at provisioning and which can be changed on the volume afterwards.
func TestCheckDataVolumes_ReadsTheBoundVolumeRatherThanTheClass(t *testing.T) {
	t.Parallel()

	database := platform.StorageClassDatabase

	volume := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              database,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		},
	}

	result := resultFor(t, dataClaimsRunner(t, volume, dataClaim("data-pg-0", &database, volume.Name)).Run(context.Background()), "holding data")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status,
		"the bound volume reclaims Delete, so deleting the claim deletes it, whatever the class says")
}

func TestCheckDataVolumes_PassesAClaimOnTheRetainingClass(t *testing.T) {
	t.Parallel()

	database := platform.StorageClassDatabase

	result := resultFor(t, dataClaimsRunner(t, dataClaim("data-pg-0", &database, "")).Run(context.Background()), "holding data")

	assert.Equal(t, clustersmoke.StatusPassed, result.Status, result.Detail)
}

// discoveryServer answers discovery the way an API server does when an
// aggregated group is registered: /apis lists it, and its version answers
// with the given status.
func discoveryServer(t *testing.T, externalMetrics int, listed bool) *httptest.Server {
	t.Helper()

	groups := []metav1.APIGroup{}

	if listed {
		version := metav1.GroupVersionForDiscovery{
			GroupVersion: clustersmoke.ExternalMetricsGroup + "/v1beta1",
			Version:      "v1beta1",
		}
		groups = append(groups, metav1.APIGroup{
			Name: clustersmoke.ExternalMetricsGroup, Versions: []metav1.GroupVersionForDiscovery{version},
			PreferredVersion: version,
		})
	}

	reply := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body) // a test server; the client reports what it got
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			reply(w, http.StatusOK, metav1.APIVersions{Versions: []string{"v1"}})
		case "/api/v1":
			reply(w, http.StatusOK, metav1.APIResourceList{GroupVersion: "v1"})
		case "/apis":
			reply(w, http.StatusOK, metav1.APIGroupList{Groups: groups})
		case "/apis/" + clustersmoke.ExternalMetricsGroup + "/v1beta1":
			reply(w, externalMetrics, metav1.APIResourceList{GroupVersion: clustersmoke.ExternalMetricsGroup + "/v1beta1"})
		default:
			reply(w, http.StatusNotFound, metav1.Status{})
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// TestCheckExternalMetrics_FailsAGroupThatIsRegisteredAndNotAnswering drives
// the check through client-go's real discovery rather than a hand-built error
// string. ServerGroups, which it used, drops the groups that failed and still
// lists their names, so a registered group that answered 503 read as served —
// the one case the check exists for could not fail.
func TestCheckExternalMetrics_FailsAGroupThatIsRegisteredAndNotAnswering(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status int
		listed bool
		want   clustersmoke.Status
	}{
		"registered and not answering": {http.StatusServiceUnavailable, true, clustersmoke.StatusFailed},
		"served":                       {http.StatusOK, true, clustersmoke.StatusPassed},
		"not installed":                {http.StatusOK, false, clustersmoke.StatusSkipped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, err := kubernetes.NewForConfig(&rest.Config{Host: discoveryServer(t, tc.status, tc.listed).URL})
			require.NoError(t, err)

			smoke := clustersmoke.NewWithClient(client, nil, clustersmoke.Options{
				StorageClass: platform.StorageClass,
				BindTimeout:  testBindTimeout,
			})

			result := resultFor(t, smoke.Run(context.Background()), clustersmoke.CheckExternalMetrics)

			assert.Equal(t, tc.want, result.Status, result.Detail)
		})
	}
}

// labelledNamespace is a namespace the policy-controller admits.
func labelledNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{charts.PolicyControllerIncludeLabel: charts.PolicyControllerIncludeValue},
	}}
}

// admissionAnswers makes a pod create return what the API server would, and
// records where it was tried and whether it was a dry run.
func admissionAnswers(err error, seen *[]string, dryRun *bool) func(*fake.Clientset) {
	return admissionAnswersPod(err, seen, dryRun, nil)
}

// admissionAnswersPod is admissionAnswers, also keeping the pod submitted.
func admissionAnswersPod(err error, seen *[]string, dryRun *bool, pod **corev1.Pod) func(*fake.Clientset) {
	return func(client *fake.Clientset) {
		client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
			create, ok := action.(k8stesting.CreateActionImpl)
			if !ok {
				return false, nil, nil
			}

			*seen = append(*seen, create.GetNamespace())
			*dryRun = len(create.CreateOptions.DryRun) == 1 && create.CreateOptions.DryRun[0] == metav1.DryRunAll

			if submitted, ok := create.GetObject().(*corev1.Pod); ok && pod != nil {
				*pod = submitted
			}

			return true, create.GetObject(), err
		})
	}
}

func TestCheckImageAdmission(t *testing.T) {
	t.Parallel()

	denied := apierrors.NewBadRequest(`admission webhook "` + charts.PolicyControllerWebhookName +
		`" denied the request: validation failed: no matching policies: spec.containers[0].image`)

	for name, tc := range map[string]struct {
		objects []runtime.Object
		answer  error
		want    clustersmoke.Status
		tried   []string
	}{
		"refused by the policy-controller": {
			objects: []runtime.Object{labelledNamespace("traefik"), labelledNamespace("argocd")},
			answer:  denied,
			want:    clustersmoke.StatusPassed,
			tried:   []string{"argocd"},
		},
		"admitted": {
			objects: []runtime.Object{labelledNamespace("traefik")},
			want:    clustersmoke.StatusFailed,
			tried:   []string{"traefik"},
		},
		"webhook unreachable": {
			objects: []runtime.Object{labelledNamespace("traefik")},
			answer:  apierrors.NewInternalError(errors.New(`failed calling webhook "policy.sigstore.dev": context deadline exceeded`)),
			want:    clustersmoke.StatusFailed,
			tried:   []string{"traefik"},
		},
		"no namespace labelled": {
			objects: []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "traefik"}}},
			want:    clustersmoke.StatusSkipped,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				tried  []string
				dryRun bool
			)

			r := runner(t, tc.objects, admissionAnswers(tc.answer, &tried, &dryRun))
			result := resultFor(t, r.Run(context.Background()), "outside the inventory")

			assert.Equal(t, tc.want, result.Status, result.Detail)
			assert.Equal(t, tc.tried, tried, "the first labelled namespace, by name")

			if len(tried) > 0 {
				assert.True(t, dryRun, "the probe must be a dry run, or it creates a pod")
			}
		})
	}
}

// The probe pod meets Pod Security's restricted level, so the API server has
// nothing to warn about — a warning printed in the middle of the results.
func TestCheckImageAdmission_SubmitsARestrictedPod(t *testing.T) {
	t.Parallel()

	var (
		tried  []string
		dryRun bool
		pod    *corev1.Pod
	)

	r := runner(t, []runtime.Object{labelledNamespace("traefik")}, admissionAnswersPod(nil, &tried, &dryRun, &pod))
	r.Run(context.Background())

	require.NotNil(t, pod)
	require.NotNil(t, pod.Spec.SecurityContext)
	assert.True(t, *pod.Spec.SecurityContext.RunAsNonRoot)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, pod.Spec.SecurityContext.SeccompProfile.Type)

	container := pod.Spec.Containers[0].SecurityContext
	require.NotNil(t, container)
	assert.False(t, *container.AllowPrivilegeEscalation)
	assert.Equal(t, []corev1.Capability{"ALL"}, container.Capabilities.Drop)
}
