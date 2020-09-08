package main

import (
	"flag"
	"time"

	"github.com/ellistarn/karpenter/pkg/apis"
	"github.com/ellistarn/karpenter/pkg/controllers"
	controllershorizontalautoscalerv1alpha1 "github.com/ellistarn/karpenter/pkg/controllers/horizontalautoscaler/v1alpha1"
	controllersmetricsproducerv1alpha1 "github.com/ellistarn/karpenter/pkg/controllers/horizontalautoscaler/v1alpha1"
	controllersscalablenodegroupv1alpha1 "github.com/ellistarn/karpenter/pkg/controllers/horizontalautoscaler/v1alpha1"
	"github.com/ellistarn/karpenter/pkg/metrics/producers"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	controllerruntime "sigs.k8s.io/controller-runtime"
	controllerruntimezap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	// +kubebuilder:scaffold:imports
)

var (
	scheme       = runtime.NewScheme()
	options      = Options{}
	dependencies = Dependencies{}
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apis.AddToScheme(scheme)
}

// Options for running this binary
type Options struct {
	EnableLeaderElection bool
	EnableWebhook        bool
	EnableController     bool
	EnableVerboseLogging bool
	MetricsAddr          string
}

// Dependencies to be injected
type Dependencies struct {
	Manager                manager.Manager
	InformerFactory        informers.SharedInformerFactory
	Controllers            []controllers.Controller
	MetricsProducerFactory producers.MetricsProducerFactory
}

func main() {
	dependencies.Manager = managerOrDie()
	dependencies.InformerFactory = informerFactoryOrDie()
	dependencies.Controllers = controllersOrDie()
	dependencies.MetricsProducerFactory = metricsProducerFactoryOrDie()

	if err := dependencies.Manager.Start(controllerruntime.SetupSignalHandler()); err != nil {
		zap.S().Fatalf("Unable to start manager, %v", err)
	}
}

func setupFlags() {
	flag.BoolVar(&options.EnableLeaderElection, "enable-leader-election", true, "Enable leader election.")
	flag.BoolVar(&options.EnableWebhook, "enable-webhook", true, "Enable webhook.")
	flag.BoolVar(&options.EnableController, "enable-controller", true, "Enable controller.")
	flag.BoolVar(&options.EnableVerboseLogging, "verbose", true, "Enable verbose logging.")
	flag.StringVar(&options.MetricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.Parse()
}

func setupLogger() {
	logger := controllerruntimezap.NewRaw(controllerruntimezap.UseDevMode(options.EnableVerboseLogging))
	controllerruntime.SetLogger(zapr.NewLogger(logger))
	zap.ReplaceGlobals(logger)
}

func managerOrDie() manager.Manager {
	mgr, err := controllerruntime.NewManager(controllerruntime.GetConfigOrDie(), controllerruntime.Options{
		Scheme:             scheme,
		MetricsBindAddress: options.MetricsAddr,
		Port:               9443,
		LeaderElection:     options.EnableLeaderElection,
		LeaderElectionID:   "karpenter-leader-election",
	})
	if err != nil {
		zap.S().Fatalf("Unable to start controller manager, %v", err)
	}
	return mgr
}

func informerFactoryOrDie() informers.SharedInformerFactory {
	factory := informers.NewSharedInformerFactory(
		kubernetes.NewForConfigOrDie(dependencies.Manager.GetConfig()),
		time.Minute*30,
	)

	if err := dependencies.Manager.Add(manager.RunnableFunc(func(stopChannel <-chan struct{}) error {
		factory.Start(stopChannel)
		<-stopChannel
		return nil
	})); err != nil {
		zap.S().Fatalf("Unable to register informer factory, %v", err)
	}

	return factory
}

func controllersOrDie() []controllers.Controller {
	controllers := []controllers.Controller{
		&controllershorizontalautoscalerv1alpha1.Controller{
			Client: dependencies.Manager.GetClient(),
		},
		&controllersscalablenodegroupv1alpha1.Controller{
			Client: dependencies.Manager.GetClient(),
		},
		&controllersmetricsproducerv1alpha1.Controller{
			Client: dependencies.Manager.GetClient(),
		},
	}
	for _, controller := range controllers {
		if options.EnableController {
			var builder = controllerruntime.NewControllerManagedBy(dependencies.Manager).For(controller.For())
			for _, resource := range controller.Owns() {
				builder = builder.Owns(resource)
			}
			if err := builder.Complete(controller); err != nil {
				zap.S().Fatalf("Unable to create controller for resource %v, %v", controller.For(), err)
			}
		}

		if options.EnableWebhook {
			if err := controllerruntime.
				NewWebhookManagedBy(dependencies.Manager).
				For(controller.For()).
				Complete(); err != nil {
				zap.S().Fatalf("Unable to create webhook for resource %v, %v", controller.For(), err)
			}
		}
	}
	return controllers
}

func metricsProducerFactoryOrDie() producers.MetricsProducerFactory {
	return producers.MetricsProducerFactory{
		InformerFactory: dependencies.InformerFactory,
	}
}