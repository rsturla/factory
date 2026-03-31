// Package kubernetes implements compute.Provider for Kubernetes/OpenShift.
//
// It manages reconciler worker Deployments by adjusting replica counts.
// Each queue maps to a Deployment named "{prefix}-{queue}" in the configured namespace.
package kubernetes

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hummingbird-org/factory-workqueue/internal/compute"
)

// Config holds configuration for the Kubernetes provider.
type Config struct {
	// Namespace is the Kubernetes namespace where worker Deployments live.
	Namespace string

	// DeploymentPrefix is prepended to the queue name to form the Deployment name.
	// e.g., prefix "factory" + queue "rpm-update" → Deployment "factory-rpm-update".
	DeploymentPrefix string

	// Kubeconfig is the path to a kubeconfig file. If empty, in-cluster config is used.
	Kubeconfig string
}

// Provider implements compute.Provider for Kubernetes/OpenShift.
type Provider struct {
	client    kubernetes.Interface
	namespace string
	prefix    string
}

// New creates a new Kubernetes compute provider.
func New(cfg Config) (*Provider, error) {
	var restCfg *rest.Config
	var err error

	if cfg.Kubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	return NewWithClient(client, cfg), nil
}

// NewWithClient creates a provider with an injected Kubernetes client (for testing).
func NewWithClient(client kubernetes.Interface, cfg Config) *Provider {
	prefix := cfg.DeploymentPrefix
	if prefix == "" {
		prefix = "factory"
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = "factory"
	}
	return &Provider{
		client:    client,
		namespace: ns,
		prefix:    prefix,
	}
}

func (p *Provider) Name() string { return "kubernetes" }

func (p *Provider) deploymentName(queue string) string {
	return p.prefix + "-" + queue
}

func (p *Provider) EnsureWorkers(ctx context.Context, queue string, desired int) error {
	name := p.deploymentName(queue)
	deploy, err := p.client.AppsV1().Deployments(p.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", name, err)
	}

	replicas := int32(desired)
	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas == replicas {
		return nil // already at desired count
	}

	deploy.Spec.Replicas = &replicas
	_, err = p.client.AppsV1().Deployments(p.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("scale deployment %s to %d: %w", name, desired, err)
	}
	return nil
}

func (p *Provider) ScaleToZero(ctx context.Context, queue string) error {
	return p.EnsureWorkers(ctx, queue, 0)
}

func (p *Provider) WorkerStatus(ctx context.Context, queue string) ([]compute.WorkerInfo, error) {
	name := p.deploymentName(queue)

	// List pods matching the deployment's label selector.
	deploy, err := p.client.AppsV1().Deployments(p.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", name, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("parse selector: %w", err)
	}

	pods, err := p.client.CoreV1().Pods(p.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	var workers []compute.WorkerInfo
	for _, pod := range pods.Items {
		workers = append(workers, compute.WorkerInfo{
			ID:      pod.Name,
			Backend: "kubernetes",
			Status:  podStatus(pod),
			Metadata: map[string]string{
				"node":      pod.Spec.NodeName,
				"namespace": pod.Namespace,
				"ip":        pod.Status.PodIP,
			},
		})
	}
	return workers, nil
}

func (p *Provider) Cleanup(ctx context.Context, queue string) error {
	name := p.deploymentName(queue)
	deploy, err := p.client.AppsV1().Deployments(p.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s: %w", name, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return fmt.Errorf("parse selector: %w", err)
	}

	// Delete pods in Failed or Succeeded phase (terminated).
	pods, err := p.client.CoreV1().Pods(p.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			_ = p.client.CoreV1().Pods(p.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		}
	}
	return nil
}

func podStatus(pod corev1.Pod) string {
	switch pod.Status.Phase {
	case corev1.PodRunning:
		return "running"
	case corev1.PodPending:
		return "pending"
	default:
		return "terminating"
	}
}

// Verify interface compliance.
var _ compute.Provider = (*Provider)(nil)

// Suppress unused import warnings for appsv1.
var _ *appsv1.Deployment
