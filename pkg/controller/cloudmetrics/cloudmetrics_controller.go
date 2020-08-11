// This controller reconciles metrics for cloud resources (currently redis and postgres)
// It takes a sync the world approach, reconciling all cloud resources every set period
// of time (currently every 5 minutes)
package cloudmetrics

import (
	"context"

	"github.com/aws/aws-sdk-go/service/cloudwatch"
	"github.com/integr8ly/cloud-resource-operator/pkg/providers"
	"github.com/integr8ly/cloud-resource-operator/pkg/providers/aws"
	"github.com/integr8ly/cloud-resource-operator/pkg/resources"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"

	integreatlyv1alpha1 "github.com/integr8ly/cloud-resource-operator/pkg/apis/integreatly/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	customMetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	labelClusterIDKey   = "clusterID"
	labelResourceIDKey  = "resourceID"
	labelNamespaceKey   = "namespace"
	labelInstanceIDKey  = "instanceID"
	labelProductNameKey = "productName"
	labelStrategyKey    = "strategy"
)

// generic list of label keys used for Gauge Vectors
var labels = []string{
	labelClusterIDKey,
	labelResourceIDKey,
	labelNamespaceKey,
	labelInstanceIDKey,
	labelProductNameKey,
	labelStrategyKey,
}

// CroGaugeMetric allows for a mapping between an exposed prometheus metric and multiple cloud provider specific metric
type CroGaugeMetric struct {
	Name         string
	GaugeVec     *prometheus.GaugeVec
	ProviderType map[string]providers.CloudProviderMetricType
}

// postgresGaugeMetrics stores a mapping between an exposed (postgres) prometheus metric and multiple cloud provider specific metric
// to add any addition metrics simply add to this mapping and it will be scraped and exposed
var postgresGaugeMetrics = []CroGaugeMetric{
	{
		Name: resources.PostgresFreeStorageAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.PostgresFreeStorageAverage,
				Help: "The amount of available storage space. Units: Bytes",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.PostgresFreeStorageAverage,
				ProviderMetricName:   "FreeStorageSpace",
				Statistic:            cloudwatch.StatisticAverage,
			},
		},
	},
	{
		Name: resources.PostgresCPUUtilizationAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.PostgresCPUUtilizationAverage,
				Help: "The percentage of CPU utilization. Units: Percent",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.PostgresCPUUtilizationAverage,
				ProviderMetricName:   "CPUUtilization",
				Statistic:            cloudwatch.StatisticAverage,
			},
		},
	},
	{
		Name: resources.PostgresFreeableMemoryAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.PostgresFreeableMemoryAverage,
				Help: "The amount of available random access memory. Units: Bytes",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.PostgresFreeableMemoryAverage,
				ProviderMetricName:   "FreeableMemory",
				Statistic:            cloudwatch.StatisticAverage,
			},
		},
	},
}

// redisGaugeMetrics stores a mapping between an exposed (redis) prometheus metric and multiple cloud provider specific metric
// to add any addition metrics simply add to this mapping and it will be scraped and exposed
var redisGaugeMetrics = []CroGaugeMetric{
	{
		Name: resources.RedisMemoryUsagePercentageAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.RedisMemoryUsagePercentageAverage,
				Help: "The percentage of redis used memory. Units: Bytes",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.RedisMemoryUsagePercentageAverage,
				//calculated on used_memory/maxmemory from Redis INFO http://redis.io/commands/info
				ProviderMetricName: "DatabaseMemoryUsagePercentage",
				Statistic:          cloudwatch.StatisticAverage,
			},
		},
	},
	{
		Name: resources.RedisFreeableMemoryAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.RedisFreeableMemoryAverage,
				Help: "The amount of available random access memory. Units: Bytes",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.RedisFreeableMemoryAverage,
				ProviderMetricName:   "FreeableMemory",
				Statistic:            cloudwatch.StatisticAverage,
			},
		},
	},
	{
		Name: resources.RedisCPUUtilizationAverage,
		GaugeVec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: resources.RedisCPUUtilizationAverage,
				Help: "The percentage of CPU utilization. Units: Percent",
			},
			labels),
		ProviderType: map[string]providers.CloudProviderMetricType{
			providers.AWSDeploymentStrategy: {
				PromethuesMetricName: resources.RedisCPUUtilizationAverage,
				ProviderMetricName:   "CPUUtilization",
				Statistic:            cloudwatch.StatisticAverage,
			},
		},
	},
}

// Add creates a new CloudMetrics Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	c := mgr.GetClient()
	logger := logrus.WithFields(logrus.Fields{"controller": "controller_cloudmetrics"})
	postgresProviderList := []providers.PostgresMetricsProvider{aws.NewAWSPostgresMetricsProvider(c, logger)}
	redisProviderList := []providers.RedisMetricsProvider{aws.NewAWSRedisMetricsProvider(c, logger)}

	// we only wish to register metrics once when the new reconciler is created
	// as the metrics we want to expose are known in advance we can register them all
	// they will only be exposed if there is a value returned for the vector for a provider
	registerGaugeVectorMetrics(logger)

	return &ReconcileCloudMetrics{
		client:               mgr.GetClient(),
		scheme:               mgr.GetScheme(),
		logger:               logger,
		postgresProviderList: postgresProviderList,
		redisProviderList:    redisProviderList,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("cloudmetrics-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Set up a GenericEvent channel that will be used
	// as the event source to trigger the controller's
	// reconcile loop
	events := make(chan event.GenericEvent)

	// Send a generic event to the channel to kick
	// off the first reconcile loop
	go func() {
		events <- event.GenericEvent{
			Meta:   &integreatlyv1alpha1.Redis{},
			Object: &integreatlyv1alpha1.Redis{},
		}
	}()

	// Setup the controller to use the channel as its watch source
	err = c.Watch(&source.Channel{Source: events}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileCloudMetrics implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileCloudMetrics{}

// ReconcileCloudMetrics reconciles a CloudMetrics object
type ReconcileCloudMetrics struct {
	client               client.Client
	scheme               *runtime.Scheme
	logger               *logrus.Entry
	postgresProviderList []providers.PostgresMetricsProvider
	redisProviderList    []providers.RedisMetricsProvider
}

// reconcile reads all redis and postgres crs periodically and reconcile metrics for these
// resources.
// the Controller will requeue the Request every 5 minutes constantly when RequeueAfter is set
func (r *ReconcileCloudMetrics) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	r.logger.Info("reconciling CloudMetrics")
	ctx := context.Background()

	// scrapedMetrics stores the GenericCloudMetric which are returned from the providers
	var scrapedMetrics []*providers.GenericCloudMetric

	// fetch all redis crs
	redisInstances := &integreatlyv1alpha1.RedisList{}
	err := r.client.List(ctx, redisInstances)
	if err != nil {
		return reconcile.Result{}, err
	}

	// loop through the redis crs and scrape the related provider specific metrics
	for _, redis := range redisInstances.Items {
		r.logger.Infof("beginning to scrape metrics for redis cr: %s", redis.Name)
		for _, p := range r.redisProviderList {
			// only scrape metrics on supported strategies
			if !p.SupportsStrategy(redis.Status.Strategy) {
				continue
			}
			var redisMetricTypes []providers.CloudProviderMetricType
			for _, gaugeMetric := range redisGaugeMetrics {
				for provider, metricType := range gaugeMetric.ProviderType {
					if provider == redis.Status.Strategy {
						redisMetricTypes = append(redisMetricTypes, metricType)
						continue
					}
				}
			}

			// all redis scrapedMetric providers inherit the same interface
			// scrapeMetrics returns scraped metrics output which contains a list of GenericCloudMetrics
			scrapedMetricsOutput, err := p.ScrapeRedisMetrics(ctx, &redis, redisMetricTypes)
			if err != nil {
				r.logger.Errorf("failed to scrape metrics for redis %v", err)
				continue
			}

			scrapedMetrics = append(scrapedMetrics, scrapedMetricsOutput.Metrics...)
		}
	}
	// for each scraped metric value we check redisGaugeMetrics for a match and set the value and labels
	r.setGaugeMetrics(redisGaugeMetrics, scrapedMetrics)

	// Fetch all postgres crs
	postgresInstances := &integreatlyv1alpha1.PostgresList{}
	err = r.client.List(ctx, postgresInstances)
	if err != nil {
		r.logger.Error(err)
	}
	for _, postgres := range postgresInstances.Items {
		r.logger.Infof("beginning to scrape metrics for postgres cr: %s", postgres.Name)
		for _, p := range r.postgresProviderList {
			// only scrape metrics on supported strategies
			if !p.SupportsStrategy(postgres.Status.Strategy) {
				continue
			}

			// filter out the provider specific metric from the postgresGaugeMetrics map which defines the metrics we want to scrape
			var postgresMetricTypes []providers.CloudProviderMetricType
			for _, gaugeMetric := range postgresGaugeMetrics {
				for provider, metricType := range gaugeMetric.ProviderType {
					if provider == postgres.Status.Strategy {
						postgresMetricTypes = append(postgresMetricTypes, metricType)
						continue
					}
				}
			}

			// all postgres scrapedMetric providers inherit the same interface
			// scrapeMetrics returns scraped metrics output which contains a list of GenericCloudMetrics
			scrapedMetricsOutput, err := p.ScrapePostgresMetrics(ctx, &postgres, postgresMetricTypes)
			if err != nil {
				r.logger.Errorf("failed to scrape metrics for postgres %v", err)
				continue
			}

			// add the returned scraped metrics to the list of metrics
			scrapedMetrics = append(scrapedMetrics, scrapedMetricsOutput.Metrics...)
		}
	}

	// for each scraped metric value we check postgresGaugeMetrics for a match and set the value and labels
	r.setGaugeMetrics(postgresGaugeMetrics, scrapedMetrics)

	// we want full control over when we scrape metrics
	// to allow for this we only have a single requeue
	// this ensures regardless of errors or return times
	// all metrics are scraped and exposed at the same time
	return reconcile.Result{
		RequeueAfter: resources.GetMetricReconcileTimeOrDefault(resources.MetricsWatchDuration),
	}, nil
}

func registerGaugeVectorMetrics(logger *logrus.Entry) {
	for _, metric := range postgresGaugeMetrics {
		logger.Infof("registering metric: %s ", metric.Name)
		customMetrics.Registry.MustRegister(metric.GaugeVec)
	}
	for _, metric := range redisGaugeMetrics {
		logger.Infof("registering metric: %s ", metric.Name)
		customMetrics.Registry.MustRegister(metric.GaugeVec)
	}
}

// func setGaugeMetrics sets the value on exposed metrics with labels
func (r *ReconcileCloudMetrics) setGaugeMetrics(gaugeMetrics []CroGaugeMetric, scrapedMetrics []*providers.GenericCloudMetric) {
	for _, scrapedMetric := range scrapedMetrics {
		for _, croMetric := range gaugeMetrics {
			if scrapedMetric.Name == croMetric.Name {
				croMetric.GaugeVec.WithLabelValues(
					scrapedMetric.Labels[labelClusterIDKey],
					scrapedMetric.Labels[labelResourceIDKey],
					scrapedMetric.Labels[labelNamespaceKey],
					scrapedMetric.Labels[labelInstanceIDKey],
					scrapedMetric.Labels[labelProductNameKey],
					scrapedMetric.Labels[labelStrategyKey]).Set(scrapedMetric.Value)
				r.logger.Infof("successfully set metric value for %s", croMetric.Name)
				continue
			}
		}
	}
}
