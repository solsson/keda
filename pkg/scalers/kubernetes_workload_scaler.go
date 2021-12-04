package scalers

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kedautil "github.com/kedacore/keda/v2/pkg/util"
)

type kubernetesWorkloadScaler struct {
	metadata   *kubernetesWorkloadMetadata
	kubeClient client.Client
}

const (
	kubernetesWorkloadMetricType = "External"
	podSelectorKey               = "podSelector"
	valueKey                     = "value"
)

var countIgnoresPhases = []corev1.PodPhase{
	corev1.PodSucceeded,
	corev1.PodFailed,
	corev1.PodUnknown,
}

type kubernetesWorkloadMetadata struct {
	podSelector labels.Selector
	namespace   string
	value       int64
	scalerIndex int
}

// NewKubernetesWorkloadScaler creates a new kubernetesWorkloadScaler
func NewKubernetesWorkloadScaler(kubeClient client.Client, config *ScalerConfig) (Scaler, error) {
	meta, parseErr := parseWorkloadMetadata(config)
	if parseErr != nil {
		return nil, fmt.Errorf("error parsing kubernetes workload metadata: %s", parseErr)
	}

	return &kubernetesWorkloadScaler{
		metadata:   meta,
		kubeClient: kubeClient,
	}, nil
}

func parseWorkloadMetadata(config *ScalerConfig) (*kubernetesWorkloadMetadata, error) {
	meta := &kubernetesWorkloadMetadata{}
	var err error
	meta.namespace = config.Namespace
	meta.podSelector, err = labels.Parse(config.TriggerMetadata[podSelectorKey])
	if err != nil || meta.podSelector.String() == "" {
		return nil, fmt.Errorf("invalid pod selector")
	}
	meta.value, err = strconv.ParseInt(config.TriggerMetadata[valueKey], 10, 64)
	if err != nil || meta.value == 0 {
		return nil, fmt.Errorf("value must be an integer greater than 0")
	}
	meta.scalerIndex = config.ScalerIndex
	return meta, nil
}

// IsActive determines if we need to scale from zero
func (s *kubernetesWorkloadScaler) IsActive(ctx context.Context) (bool, error) {
	pods, err := s.getMetricValue(ctx)

	if err != nil {
		return false, err
	}

	return pods > 0, nil
}

// Close no need for kubernetes workload scaler
func (s *kubernetesWorkloadScaler) Close(context.Context) error {
	return nil
}

// GetMetricSpecForScaling returns the metric spec for the HPA
func (s *kubernetesWorkloadScaler) GetMetricSpecForScaling(context.Context) []v2beta2.MetricSpec {
	targetMetricValue := resource.NewQuantity(s.metadata.value, resource.DecimalSI)
	externalMetric := &v2beta2.ExternalMetricSource{
		Metric: v2beta2.MetricIdentifier{
			Name: GenerateMetricNameWithIndex(s.metadata.scalerIndex, kedautil.NormalizeString(fmt.Sprintf("workload-%s", s.metadata.namespace))),
		},
		Target: v2beta2.MetricTarget{
			Type:         v2beta2.AverageValueMetricType,
			AverageValue: targetMetricValue,
		},
	}
	metricSpec := v2beta2.MetricSpec{External: externalMetric, Type: kubernetesWorkloadMetricType}
	return []v2beta2.MetricSpec{metricSpec}
}

// GetMetrics returns value for a supported metric
func (s *kubernetesWorkloadScaler) GetMetrics(ctx context.Context, metricName string, metricSelector labels.Selector) ([]external_metrics.ExternalMetricValue, error) {
	pods, err := s.getMetricValue(ctx)
	if err != nil {
		return []external_metrics.ExternalMetricValue{}, fmt.Errorf("error inspecting kubernetes workload: %s", err)
	}

	metric := external_metrics.ExternalMetricValue{
		MetricName: metricName,
		Value:      *resource.NewQuantity(int64(pods), resource.DecimalSI),
		Timestamp:  metav1.Now(),
	}

	return append([]external_metrics.ExternalMetricValue{}, metric), nil
}

func (s *kubernetesWorkloadScaler) getMetricValue(ctx context.Context) (int, error) {
	podList := &corev1.PodList{}
	listOptions := client.ListOptions{}
	listOptions.LabelSelector = s.metadata.podSelector
	listOptions.Namespace = s.metadata.namespace
	opts := []client.ListOption{
		&listOptions,
	}

	err := s.kubeClient.List(ctx, podList, opts...)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, pod := range podList.Items {
		count += getCountValue(pod)
	}

	return count, nil
}

func getCountValue(pod corev1.Pod) int {
	for _, ignore := range countIgnoresPhases {
		if pod.Status.Phase == ignore {
			return 0
		}
	}
	return 1
}
