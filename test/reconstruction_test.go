//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func int32Ptr(i int32) *int32 { return &i }

// deployments used for testing
var testDeployment = &appsv1.Deployment{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "test-app",
		Namespace: "default",
	},
	Spec: appsv1.DeploymentSpec{
		Replicas: int32Ptr(3),
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "test-app"},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"app": "test-app"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app",
						Image: "nginx:1",
					},
				},
			},
		},
	},
}

// expectedrevisions-to be checked everytime with the reconstructed version
var expectedRevisions = map[int]struct {
	image    string
	replicas int32
}{
	1: {image: "nginx:1", replicas: 3},
	2: {image: "nginx:1.10", replicas: 3},
	3: {image: "nginx:1.11", replicas: 5},
}

type ReconstructResponse struct {
	Data struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name        string `json:"name"`
			Namespace   string `json:"namespace"`
			Annotations struct {
			} `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			ProgressDeadlineSeconds int   `json:"progressDeadlineSeconds"`
			Replicas                int32 `json:"replicas"`
			RevisionHistoryLimit    int   `json:"revisionHistoryLimit"`
			Selector                struct {
				MatchLabels struct {
					App string `json:"app"`
				} `json:"matchLabels"`
			} `json:"selector"`
			Strategy struct {
				RollingUpdate struct {
					MaxSurge       string `json:"maxSurge"`
					MaxUnavailable string `json:"maxUnavailable"`
				} `json:"rollingUpdate"`
				Type string `json:"type"`
			} `json:"strategy"`
			Template struct {
				Metadata struct {
					CreationTimestamp interface{} `json:"creationTimestamp"`
					Labels            struct {
						App string `json:"app"`
					} `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Containers []struct {
						Image           string `json:"image"`
						ImagePullPolicy string `json:"imagePullPolicy"`
						Name            string `json:"name"`
						Resources       struct {
						} `json:"resources"`
						TerminationMessagePath   string `json:"terminationMessagePath"`
						TerminationMessagePolicy string `json:"terminationMessagePolicy"`
					} `json:"containers"`
					DNSPolicy       string `json:"dnsPolicy"`
					RestartPolicy   string `json:"restartPolicy"`
					SchedulerName   string `json:"schedulerName"`
					SecurityContext struct {
					} `json:"securityContext"`
					TerminationGracePeriodSeconds int `json:"terminationGracePeriodSeconds"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	} `json:"data"`
}

func TestDeploymentTracking(t *testing.T) {

	// creating the deployment uuid
	var deploymentUID string

	feature := features.New("Deployment Tracking")

	// create the deployment
	feature.Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

		resp, err := http.Get("http://localhost:9090/healthz")
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("API not reachable - port forward may not be working: %v", err)
		}
		t.Log("API is reachable ✅")

		log.Println("Creating the deployment")

		client := cfg.Client()

		appsv1.AddToScheme(client.Resources().GetScheme())

		if err := client.Resources().Create(ctx, testDeployment); err != nil {
			t.Fatalf("failed to create deployment: %s", err)
		}

		// wait for deployment to be available
		log.Println("Wait for deployment to be available")
		if err := wait.For(
			conditions.New(client.Resources()).DeploymentAvailable("test-app", "default"),
			wait.WithTimeout(2*time.Minute),
			wait.WithInterval(10*time.Second),
		); err != nil {
			t.Fatalf("deployment never became available: %s", err)
		}

		var created appsv1.Deployment
		if err := client.Resources().Get(ctx, "test-app", "default", &created); err != nil {
			t.Fatalf("failed to get deployment uid: %s", err)
		}
		deploymentUID = string(created.UID)
		t.Logf("deployment created with UID: %s", deploymentUID)

		return ctx
	})

	// update image to nginx:1.10
	feature.Assess("update image to nginx:1.10", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		log.Println("update image to nginx:1.10")

		client := cfg.Client()

		var deployment appsv1.Deployment
		if err := client.Resources().Get(ctx, "test-app", "default", &deployment); err != nil {
			t.Fatalf("failed to get deployment: %s", err)
		}

		// update the image
		deployment.Spec.Template.Spec.Containers[0].Image = expectedRevisions[2].image
		if err := client.Resources().Update(ctx, &deployment); err != nil {
			t.Fatalf("failed to update deployment image: %s", err)
		}

		time.Sleep(10 * time.Second)

		return ctx
	})

	// increase number of replicas
	feature.Assess("increase number of replicas", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		log.Println("Increasing number of Replicas")

		client := cfg.Client()

		var deployment appsv1.Deployment
		if err := client.Resources().Get(ctx, "test-app", "default", &deployment); err != nil {
			t.Fatalf("failed to get deployment: %s", err)
		}

		deployment.Spec.Replicas = int32Ptr(expectedRevisions[3].replicas)
		if err := client.Resources().Update(ctx, &deployment); err != nil {
			t.Fatalf("failed to scale deployment: %s", err)
		}

		time.Sleep(10 * time.Second)
		log.Printf("number of replicas is %d", &deployment.Spec.Replicas)

		return ctx
	})

	// reconstruct revision 1
	feature.Assess("reconstruct revision 1 matches expected state", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		log.Println("Reconstructing Revision 1")

		reconstructed := reconstruct(t, deploymentUID, 1)
		expected := expectedRevisions[1]

		// verify the reconstructed spec
		if reconstructed.Data.Spec.Template.Spec.Containers[0].Image != expected.image {
			t.Fatalf("revision 1 image: expected %s got %s",
				expected.image,
				reconstructed.Data.Spec.Template.Spec.Containers[0].Image,
			)
		}
		if reconstructed.Data.Spec.Replicas != expected.replicas {
			t.Fatalf("revision 1 replicas: expected %d got %d",
				expected.replicas,
				reconstructed.Data.Spec.Replicas,
			)
		}

		t.Logf("revision 1 reconstruction correct")
		return ctx
	})

	// reconstruct revision 2
	feature.Assess("reconstruct revision 2 matches expected state", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		log.Println("Reconstructing Revision 2")

		reconstructed := reconstruct(t, deploymentUID, 2)
		expected := expectedRevisions[2]

		// verify 2nd revision
		if reconstructed.Data.Spec.Template.Spec.Containers[0].Image != expected.image {
			t.Fatalf("revision 1 image: expected %s got %s",
				expected.image,
				reconstructed.Data.Spec.Template.Spec.Containers[0].Image,
			)
		}

		t.Logf("revision 2 reconstruction correct ")
		return ctx
	})

	// cleanup the resources
	feature.Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

		log.Println("Cleaning the Resources")

		client := cfg.Client()
		if err := client.Resources().Delete(ctx, testDeployment); err != nil {
			t.Logf("warning: failed to delete deployment: %s", err)
		}
		return ctx
	})

	testenv.Test(t, feature.Feature())
}

// call the reconstruct API
func reconstruct(t *testing.T, uid string, revision int) ReconstructResponse {
	t.Helper()
	url := fmt.Sprintf("http://localhost:9090/api/v1/resources/%s/reconstruct/%d", uid, revision)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("failed to call reconstruct API: %s", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	buf := bytes.NewBuffer(body)

	var result ReconstructResponse
	if err := json.NewDecoder(buf).Decode(&result); err != nil {
		t.Fatalf("failed to decode reconstruct response: %s", err)
	}

	return result
}
