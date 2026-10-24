// Package edgetest builds a fake edge fleet for tests and demos.
package edgetest

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Now is the fixed "current time" the fleet is built around.
var Now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func node(name, site string, ready bool, heartbeatAgo time.Duration, extra corev1.ResourceList) *corev1.Node {
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionUnknown
	}
	alloc := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}
	for k, v := range extra {
		alloc[k] = v
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"topology.kubernetes.io/zone": site, "node-role.kubernetes.io/edge": "",
		}},
		Status: corev1.NodeStatus{
			Capacity:    alloc,
			Allocatable: alloc,
			Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: status,
				LastHeartbeatTime: metav1.NewTime(Now.Add(-heartbeatAgo)),
			}},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.1", OSImage: "Ubuntu 24.04 LTS", Architecture: "amd64"},
		},
	}
}

func pod(ns, name, nodeName string, phase corev1.PodPhase, ready bool, waitingReason string, restarts int32) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: "app", Ready: ready, RestartCount: restarts}
	if waitingReason != "" {
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waitingReason}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

// ownedBy marks a pod as controlled by a workload of the given kind.
func ownedBy(p *corev1.Pod, kind, name string) *corev1.Pod {
	yes := true
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}}
	return p
}

// labeled sets one label on a pod.
func labeled(p *corev1.Pod, k, v string) *corev1.Pod {
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	p.Labels[k] = v
	return p
}

// withEmptyDir gives a pod an emptyDir volume (local storage that a drain would lose).
func withEmptyDir(p *corev1.Pod) *corev1.Pod {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	return p
}

// withAccel makes a pod request n of an accelerator resource.
func withAccel(p *corev1.Pod, name corev1.ResourceName, n string) *corev1.Pod {
	rl := corev1.ResourceList{name: resource.MustParse(n)}
	p.Spec.Containers[0].Resources = corev1.ResourceRequirements{Requests: rl, Limits: rl}
	return p
}

// rolloutPod is a model-runtime DaemonSet pod; image is "v1" or "v2".
func rolloutPod(nodeName, image string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	p := pod("edge-system", "model-runtime-"+nodeName, nodeName, phase, ready, "", 0)
	p.Labels = map[string]string{"app": "model-runtime"}
	ownedBy(p, "DaemonSet", "model-runtime")
	p.Spec.Containers[0].Image = "registry.local/model-runtime:" + image
	if phase == corev1.PodPending {
		p.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}
	}
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}}
	return p
}

func modelRuntime(desired, ready int32) *appsv1.DaemonSet {
	labels := map[string]string{"app": "model-runtime"}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "edge-system", Name: "model-runtime"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.local/model-runtime:v2"}}},
			},
		},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, NumberReady: ready, NumberAvailable: ready},
	}
}

func detector(p *corev1.Pod) *corev1.Pod {
	return ownedBy(labeled(p, "app", "defect-detector"), "ReplicaSet", "defect-detector-5c7b")
}

func deployment(ns, name string, desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready, AvailableReplicas: ready},
	}
}

// Fleet returns a fake clientset holding a small edge fleet:
//
//   - factory-pune: 2 nodes with Intel iGPUs, both healthy
//   - store-blr: 1 node with an Intel NPU, healthy, plus 1 node that went offline 9 minutes ago
//   - warehouse-del: 1 healthy CPU-only node
//
// The vision app is degraded (2/3 ready) because one pod is in CrashLoopBackOff.
// The edge-system/model-runtime DaemonSet is mid-rollout from v1 to v2: three nodes
// run v2, blr-02 (offline) is stuck on v1 and del-01's v2 pod is still Pending.
func Fleet() *fake.Clientset {
	gpu := corev1.ResourceList{"gpu.intel.com/i915": resource.MustParse("1")}
	npu := corev1.ResourceList{"npu.intel.com/accel": resource.MustParse("1")}
	cs := fake.NewClientset(
		node("pune-01", "factory-pune", true, 10*time.Second, gpu),
		node("pune-02", "factory-pune", true, 15*time.Second, gpu),
		node("blr-01", "store-blr", true, 8*time.Second, npu),
		node("blr-02", "store-blr", false, 9*time.Minute, nil),
		node("del-01", "warehouse-del", true, 12*time.Second, nil),

		modelRuntime(5, 3),
		rolloutPod("pune-01", "v2", corev1.PodRunning, true),
		rolloutPod("pune-02", "v2", corev1.PodRunning, true),
		rolloutPod("blr-01", "v2", corev1.PodRunning, true),
		rolloutPod("blr-02", "v1", corev1.PodRunning, false),
		rolloutPod("del-01", "v2", corev1.PodPending, false),

		deployment("vision", "defect-detector", 3, 2),
		deployment("retail", "shelf-scanner", 1, 1),

		detector(withAccel(pod("vision", "defect-detector-a", "pune-01", corev1.PodRunning, true, "", 0), "gpu.intel.com/i915", "1")),
		detector(pod("vision", "defect-detector-b", "pune-02", corev1.PodRunning, true, "", 0)),
		detector(pod("vision", "defect-detector-c", "pune-02", corev1.PodRunning, false, "CrashLoopBackOff", 7)),
		withEmptyDir(ownedBy(pod("retail", "shelf-scanner-a", "blr-01", corev1.PodRunning, true, "", 0), "ReplicaSet", "shelf-scanner-6d8f")),

		// The detector is already degraded (2/3), so its budget allows no further disruption.
		&policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Namespace: "vision", Name: "defect-detector"},
			Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "defect-detector"}}},
			Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
		},

		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "vision", Name: "ev1", CreationTimestamp: metav1.NewTime(Now.Add(-time.Minute))},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "vision", Name: "defect-detector-c"},
			Type:           corev1.EventTypeWarning, Reason: "BackOff", Message: "Back-off restarting failed container app",
			LastTimestamp: metav1.NewTime(Now.Add(-time.Minute)), Count: 7,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev2", CreationTimestamp: metav1.NewTime(Now.Add(-9 * time.Minute))},
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "blr-02"},
			Type:           corev1.EventTypeNormal, Reason: "NodeNotReady", Message: "Node blr-02 status is now: NodeNotReady",
			LastTimestamp: metav1.NewTime(Now.Add(-9 * time.Minute)),
		},
	)
	cs.PrependReactor("create", "pods", evictionReactor(cs))
	cs.PrependReactor("patch", "*", dryRunPatchReactor(cs))
	return cs
}

// evictionReactor makes the fake Eviction API behave like the real one: a pod covered by a
// PodDisruptionBudget with no disruptions left gets 429, a dry run changes nothing, and an
// accepted eviction deletes the pod.
func evictionReactor(cs *fake.Clientset) k8stesting.ReactionFunc {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ev := a.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction)
		pod, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), a.GetNamespace(), ev.Name)
		if err != nil {
			return true, nil, err
		}
		// Use the tracker, not the clientset: the fake already holds its lock while reacting.
		list, err := cs.Tracker().List(policyv1.SchemeGroupVersion.WithResource("poddisruptionbudgets"), policyv1.SchemeGroupVersion.WithKind("PodDisruptionBudget"), a.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		for _, pdb := range list.(*policyv1.PodDisruptionBudgetList).Items {
			sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err == nil && sel.Matches(labels.Set(pod.(*corev1.Pod).Labels)) && pdb.Status.DisruptionsAllowed <= 0 {
				return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 1)
			}
		}
		if ev.DeleteOptions != nil && len(ev.DeleteOptions.DryRun) > 0 {
			return true, nil, nil
		}
		return true, nil, cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), a.GetNamespace(), ev.Name)
	}
}

// dryRunPatchReactor makes the fake honour server-side dry run for patches: the object is
// returned as it is and nothing is stored, like a real API server.
func dryRunPatchReactor(cs *fake.Clientset) k8stesting.ReactionFunc {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		p, ok := a.(k8stesting.PatchActionImpl)
		if !ok || len(p.GetPatchOptions().DryRun) == 0 {
			return false, nil, nil
		}
		obj, err := cs.Tracker().Get(a.GetResource(), a.GetNamespace(), p.GetName())
		return true, obj, err
	}
}
