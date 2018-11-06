package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/spotahome/kooper/log"
	"github.com/spotahome/kooper/operator/controller"
	"github.com/spotahome/kooper/operator/handler"
	"github.com/spotahome/kooper/operator/retrieve"
	"github.com/tuannvm/tools/pkg/awsutils"
	extensionv1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// r53.kubernetes.io/enable: true
func main() {

	client := awsutils.New(&aws.Config{})
	// Initialize logger.
	log := &log.Std{}

	k8scfg, err := rest.InClusterConfig()
	if err != nil {
		// No in cluster? letr's try locally
		kubehome := filepath.Join(homedir.HomeDir(), ".kube", "config")
		k8scfg, err = clientcmd.BuildConfigFromFlags("", kubehome)
		if err != nil {
			log.Errorf("error loading kubernetes configuration: %s", err)
			os.Exit(1)
		}
	}
	k8scli, err := kubernetes.NewForConfig(k8scfg)
	if err != nil {
		log.Errorf("error creating kubernetes client: %s", err)
		os.Exit(1)
	}

	// Create our retriever so the controller knows how to get/listen for pod events.
	// atm there is no way to do something like annotation selector as annotations are unqueriable by design
	retr := &retrieve.Resource{
		Object: &extensionv1beta1.Ingress{},
		ListerWatcher: &cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return k8scli.ExtensionsV1beta1().Ingresses("").List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return k8scli.ExtensionsV1beta1().Ingresses("").Watch(options)
			},
		},
	}

	// Our domain logic that will print every add/sync/update and delete event we .
	hand := &handler.HandlerFunc{
		AddFunc: func(_ context.Context, obj runtime.Object) error {
			ingress := obj.(*extensionv1beta1.Ingress)
			for k, v := range ingress.Annotations {
				if k == "r53.kubernetes.io/enable" && v == "true" {
					client.Route53.CreateHealthCheckWithContext(client.Context, &route53.CreateHealthCheckInput{
						HealthCheckConfig: &route53.HealthCheckConfig{},
					})
					log.Infof("healthcheck added: %s/%s", ingress.Annotations, ingress.Name)
				}
			}
			return nil
		},
		DeleteFunc: func(_ context.Context, s string) error {
			log.Infof("healthcheck deleted: %s", s)
			return nil
		},
	}

	// Create the controller with custom configuration.
	cfg := &controller.Config{
		ProcessingJobRetries: 5,
		ResyncInterval:       45 * time.Second,
		ConcurrentWorkers:    1,
	}
	ctrl := controller.New(cfg, hand, retr, nil, nil, nil, log)

	// Start our controller.
	stopC := make(chan struct{})
	if err := ctrl.Run(stopC); err != nil {
		log.Errorf("error running controller: %s", err)
		os.Exit(1)
	}
	os.Exit(0)
}
