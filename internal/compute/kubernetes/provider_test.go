package kubernetes

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func seedDeployment(t *testing.T, cs *fake.Clientset, ns, name string, replicas int32) {
	t.Helper()
	_, err := cs.AppsV1().Deployments(ns).Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "busybox"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed deployment %s: %v", name, err)
	}
}

func seedPod(t *testing.T, cs *fake.Clientset, ns, name, deployName string, phase corev1.PodPhase) {
	t.Helper()
	_, err := cs.CoreV1().Pods(ns).Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app": deployName},
		},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: "worker", Image: "busybox"}},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: "10.0.0.1",
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed pod %s: %v", name, err)
	}
}

func TestNewWithClient_Defaults(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{})
	if p.namespace != "factory" {
		t.Errorf("expected default namespace factory, got %s", p.namespace)
	}
	if p.prefix != "factory" {
		t.Errorf("expected default prefix factory, got %s", p.prefix)
	}
}

func TestNewWithClient_Custom(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "prod", DeploymentPrefix: "myapp"})
	if p.namespace != "prod" {
		t.Errorf("expected namespace prod, got %s", p.namespace)
	}
	if p.prefix != "myapp" {
		t.Errorf("expected prefix myapp, got %s", p.prefix)
	}
}

func TestName(t *testing.T) {
	p := NewWithClient(fake.NewSimpleClientset(), Config{})
	if p.Name() != "kubernetes" {
		t.Errorf("expected name kubernetes, got %s", p.Name())
	}
}

func TestEnsureWorkers_ScalesDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	seedDeployment(t, cs, "ns", "wq-build", 1)

	if err := p.EnsureWorkers(context.Background(), "build", 5); err != nil {
		t.Fatalf("EnsureWorkers: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("ns").Get(context.Background(), "wq-build", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 5 {
		t.Errorf("expected 5 replicas, got %d", *deploy.Spec.Replicas)
	}
}

func TestEnsureWorkers_NoopWhenAlreadyAtDesired(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	seedDeployment(t, cs, "ns", "wq-build", 3)

	if err := p.EnsureWorkers(context.Background(), "build", 3); err != nil {
		t.Fatalf("EnsureWorkers: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("ns").Get(context.Background(), "wq-build", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("expected 3 replicas (unchanged), got %d", *deploy.Spec.Replicas)
	}
}

func TestEnsureWorkers_DeploymentNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	err := p.EnsureWorkers(context.Background(), "nonexistent", 1)
	if err == nil {
		t.Fatal("expected error for missing deployment")
	}
}

func TestScaleToZero(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	seedDeployment(t, cs, "ns", "wq-build", 5)

	if err := p.ScaleToZero(context.Background(), "build"); err != nil {
		t.Fatalf("ScaleToZero: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("ns").Get(context.Background(), "wq-build", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas, got %d", *deploy.Spec.Replicas)
	}
}

func TestWorkerStatus(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	seedDeployment(t, cs, "ns", "wq-build", 2)
	seedPod(t, cs, "ns", "wq-build-abc", "wq-build", corev1.PodRunning)
	seedPod(t, cs, "ns", "wq-build-def", "wq-build", corev1.PodPending)

	workers, err := p.WorkerStatus(context.Background(), "build")
	if err != nil {
		t.Fatalf("WorkerStatus: %v", err)
	}
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}

	statusMap := map[string]string{}
	for _, w := range workers {
		statusMap[w.ID] = w.Status
		if w.Backend != "kubernetes" {
			t.Errorf("expected backend kubernetes, got %s", w.Backend)
		}
	}
	if statusMap["wq-build-abc"] != "running" {
		t.Errorf("expected running pod to be running, got %s", statusMap["wq-build-abc"])
	}
	if statusMap["wq-build-def"] != "pending" {
		t.Errorf("expected pending pod to be pending, got %s", statusMap["wq-build-def"])
	}
}

func TestWorkerStatus_NoDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	_, err := p.WorkerStatus(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing deployment")
	}
}

func TestCleanup_RemovesTerminatedPods(t *testing.T) {
	cs := fake.NewSimpleClientset()
	p := NewWithClient(cs, Config{Namespace: "ns", DeploymentPrefix: "wq"})

	seedDeployment(t, cs, "ns", "wq-build", 1)
	seedPod(t, cs, "ns", "wq-build-running", "wq-build", corev1.PodRunning)
	seedPod(t, cs, "ns", "wq-build-failed", "wq-build", corev1.PodFailed)
	seedPod(t, cs, "ns", "wq-build-succeeded", "wq-build", corev1.PodSucceeded)

	if err := p.Cleanup(context.Background(), "build"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	pods, _ := cs.CoreV1().Pods("ns").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Errorf("expected 1 pod remaining (running), got %d", len(pods.Items))
	}
	if pods.Items[0].Name != "wq-build-running" {
		t.Errorf("expected running pod to remain, got %s", pods.Items[0].Name)
	}
}

func TestPodStatus(t *testing.T) {
	tests := []struct {
		phase    corev1.PodPhase
		expected string
	}{
		{corev1.PodRunning, "running"},
		{corev1.PodPending, "pending"},
		{corev1.PodFailed, "terminating"},
		{corev1.PodSucceeded, "terminating"},
		{corev1.PodUnknown, "terminating"},
	}
	for _, tt := range tests {
		got := podStatus(corev1.Pod{Status: corev1.PodStatus{Phase: tt.phase}})
		if got != tt.expected {
			t.Errorf("podStatus(%s) = %s, want %s", tt.phase, got, tt.expected)
		}
	}
}
