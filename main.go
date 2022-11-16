package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"os"
	"os/signal"
	"syscall"
	"time"
)

const deploymentName string = "event-count-issue-reproducer"

var deployment = &appsv1.Deployment{
	ObjectMeta: metav1.ObjectMeta{
		Name: deploymentName,
	},
	Spec: appsv1.DeploymentSpec{
		Replicas: int32Ptr(2),
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"app": "demo",
			},
		},
		Template: apiv1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app": "demo",
				},
			},
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Name:  "web",
						Image: "nginx:1.12",
						Ports: []apiv1.ContainerPort{
							{
								Name:          "http",
								Protocol:      apiv1.ProtocolTCP,
								ContainerPort: 80,
							},
						},
						Resources: apiv1.ResourceRequirements{
							Requests: apiv1.ResourceList{
								apiv1.ResourceMemory: resource.MustParse("32Gi"),
								apiv1.ResourceCPU:    resource.MustParse("500m"),
							},
							Limits: apiv1.ResourceList{
								apiv1.ResourceMemory: resource.MustParse("64Gi"),
								apiv1.ResourceCPU:    resource.MustParse("2000m"),
							},
						},
					},
				},
			},
		},
	},
}

func main() {
	clientset := getClientset()
	namespace := apiv1.NamespaceDefault

	deploymentsClient := clientset.AppsV1().Deployments(namespace)
	podsClient := clientset.CoreV1().Pods(namespace)
	eventsClient := clientset.EventsV1()

	// Create Deployment
	fmt.Println("Creating deployment...")
	result, err := deploymentsClient.Create(context.TODO(), deployment, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Error creating deployment: %s", err)
		cleanupAndExit(deploymentsClient)
	}
	fmt.Printf("Created deployment %q.\n", result.GetObjectMeta().GetName())

	// Allow OS interrupt to delete deployment before exiting
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cleanupAndExit(deploymentsClient)
		os.Exit(0)
	}()

	// Loop indefinetly, periodically checking for pod events
	for {
		// Get pods
		podList, err := podsClient.List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Error listing pods: %s", err)
			cleanupAndExit(deploymentsClient)
		}

		for _, pod := range podList.Items {
			// Get pod events
			selector, err := fields.ParseSelector(fmt.Sprintf("regarding.name=%s", pod.Name))
			if err != nil {
				fmt.Printf("failed to parse field selector: %s", err)
				cleanupAndExit(deploymentsClient)
			}
			events, err := eventsClient.Events(namespace).List(context.TODO(), metav1.ListOptions{FieldSelector: selector.String()})
			if err != nil {
				fmt.Printf("failed to get events: %s", err)
				cleanupAndExit(deploymentsClient)
			}

			for _, ev := range events.Items {
				if ev.Regarding.Kind != "Pod" {
					continue
				}

				if ev.DeprecatedCount == 0 {
					outputMsg := fmt.Sprintf("Issue is occuring - Event count is 0 for pod %s. ", pod.Name)
					if ev.Series == nil {
						outputMsg = outputMsg + "Event series is also nil"
					}
					fmt.Println(outputMsg)
				} else {
					fmt.Printf("Issue is no longer occuring - Event count is %d  for pod %s \n", ev.DeprecatedCount, pod.Name)
				}
			}
		}

		time.Sleep(time.Second * 1)
	}

}

func cleanupAndExit(deploymentsClient v1.DeploymentInterface) {
	deleteDeployment(deploymentsClient)
	fmt.Println("Exiting.")
}

func deleteDeployment(deploymentsClient v1.DeploymentInterface) {
	fmt.Println("Deleting deployment...")
	deletePolicy := metav1.DeletePropagationForeground
	if err := deploymentsClient.Delete(context.TODO(), deploymentName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		panic(err)
	}
	fmt.Println("Deleted deployment.")
}

func getClientset() *kubernetes.Clientset {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}
	return clientset
}

func int32Ptr(i int32) *int32 { return &i }
