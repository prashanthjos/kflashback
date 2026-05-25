//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/utils"
	"sigs.k8s.io/e2e-framework/support/kind"
)

var (
	testenv env.Environment

	// docker configuration
	// dockerImage = "ghcr.io/prashanthjos/kflashback:latest"
	dockerImage = "kflashback-controller:e2e"
	dockerFile  = "../Dockerfile"

	//
	crdPath  = "../config/crd/"
	rbacPath = "../config/rbac/"

	// sqlite config files
	sqliteControllerPath = "./test_data/sqlite/test-sqlite-controller-deployment.yaml"
	sqliteConfigPath     = "./test_data/sqlite/test-sqlite-config.yaml"

	// postgres config files
	postgresControllerPath = "./test_data/postgres/test-postgres-controller-deployment.yaml"
	postgresDeployPath     = "./test_data/postgres/test-postgres-deployment.yaml"
	postgresConfigPath     = "./test_data/postgres/test-postgresql-config.yaml"
	postgresDSN            = "postgres://kflashback:postgres@postgres.kflashback-system.svc.cluster.local:5432/kflashback?sslmode=disable"

	// tracking policy path
	trackPolicyPath = "./test_data/test-tracking-policy.yaml"

	// namespace config
	namespace = "kflashback-system"
)

func TestMain(m *testing.M) {

	storageBackend := os.Getenv("STORAGE_BACKEND")
	if storageBackend != "sqlite" && storageBackend != "postgresql" {
		log.Fatalf("invalid STORAGE_BACKEND %q: must be 'sqlite' or 'postgresql'", storageBackend)
	}

	testenv = env.New()
	kindClusterName := envconf.RandomName("kind-cluster", 10)

	testenv.Setup(
		envfuncs.CreateCluster(kind.NewProvider(), kindClusterName),
		envfuncs.CreateNamespace(namespace),

		// Applying CRDs
		applyCRD(),

		// Applying RBAC Policy
		applyRBAC(),

		// Apply kflashback Config for different Storage Backend
		applyKflashbackConfig(),

		// Build the Docker Image
		buildDockerImage(),
		envfuncs.LoadDockerImageToCluster(kindClusterName, dockerImage),

		deployKflashbackController(),

		// Applying tracking policy
		applyTrackingPolicy(),

		// port-forward to access api endpoints
		portForwarding(),
	)

	// Cleaning up the resources

	testenv.Finish(

		cleanUpResources(),

		envfuncs.DeleteNamespace(namespace),
		envfuncs.DestroyCluster(kindClusterName),
	)
	os.Exit(testenv.Run(m))
}

func applyCRD() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Applying CRDs")

		if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", crdPath)); p.Err() != nil {
			log.Printf("Failed to deploy kflashback: %s: %s", p.Err(), p.Out())
			return ctx, p.Err()
		}
		return ctx, nil
	}
}

func applyRBAC() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Applying RBAC Policy")

		if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", rbacPath)); p.Err() != nil {
			log.Printf("Failed to deploy kflashback RBAC Policy: %s: %s", p.Err(), p.Out())
			return ctx, p.Err()
		}

		return ctx, nil
	}
}

func applyKflashbackConfig() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {

		switch storageBackend := os.Getenv("STORAGE_BACKEND"); storageBackend {
		case "sqlite":
			log.Println("Apply SQLite kflashback Config")

			if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", sqliteConfigPath)); p.Err() != nil {
				log.Printf("Failed to deploy SQLite Policy: %s: %s", p.Err(), p.Out())
				return ctx, p.Err()
			}
		case "postgresql":

			// Step 1 - deploy postgres inside KinD
			log.Println("Deploying Postgres inside KinD")
			if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", postgresDeployPath)); p.Err() != nil {
				log.Printf("Failed to deploy Postgres: %s: %s", p.Err(), p.Out())
				return ctx, p.Err()
			}

			// Step 2 - wait for postgres to be ready
			log.Println("Waiting for Postgres to be ready")
			client := cfg.Client()
			if err := wait.For(
				conditions.New(client.Resources()).DeploymentAvailable("postgres", namespace),
				wait.WithTimeout(3*time.Minute),
				wait.WithInterval(10*time.Second),
			); err != nil {
				log.Printf("Timed out waiting for Postgres: %s", err)
				return ctx, err
			}

			// Step 3 - create the secret with DSN
			log.Println("Creating Postgres credentials secret")
			if p := utils.RunCommandContext(ctx, fmt.Sprintf(
				"kubectl create secret generic kflashback-db-credentials --namespace=%s --from-literal=dsn=%s",
				namespace,
				postgresDSN,
			)); p.Err() != nil {
				log.Printf("Failed to create secret: %s: %s", p.Err(), p.Out())
				return ctx, p.Err()
			}

			time.Sleep(10 * time.Second)

			// Step 4 - apply postgres kflashback config
			log.Println("Applying Postgres KFlashbackConfig")
			if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", postgresConfigPath)); p.Err() != nil {
				log.Printf("Failed to apply Postgres config: %s: %s", p.Err(), p.Out())
				return ctx, p.Err()
			}

			time.Sleep(10 * time.Second)

		default:
			return ctx, fmt.Errorf("unknown STORAGE_BACKEND: %q, must be 'sqlite' or 'postgresql'", storageBackend)

		}

		return ctx, nil
	}
}

func buildDockerImage() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Building the Docker Image...")

		if p := utils.RunCommandContext(ctx, fmt.Sprintf("docker build -t %s .. -f %s", dockerImage, dockerFile)); p.Err() != nil {
			log.Printf("Failed to docker image: %s: %s", p.Err(), p.Out())
			return ctx, p.Err()
		}

		// Loading the Docker Image to KinD Cluster
		log.Printf("Loading the Docker Image to KinD Cluster")

		return ctx, nil
	}
}

func deployKflashbackController() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {

		log.Println("Deploying kflashback controller")

		switch storageBackend := os.Getenv("STORAGE_BACKEND"); storageBackend {
		case "sqlite":
			if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", sqliteControllerPath)); p.Err() != nil {
				log.Printf("Failed to deploy the controller")
				return ctx, p.Err()
			}
		case "postgresql":
			if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", postgresControllerPath)); p.Err() != nil {
				log.Printf("Failed to deploy the controller")
				return ctx, p.Err()
			}
		default:
			return ctx, fmt.Errorf("unknown STORAGE_BACKEND: %q, must be 'sqlite' or 'postgresql'", storageBackend)
		}

		// waiting for the deployment to be complete
		log.Println("Waiting for the Deployment to be complete")

		client := cfg.Client()
		if err := wait.For(
			conditions.New(client.Resources()).DeploymentAvailable("kflashback-controller", namespace),
			wait.WithTimeout(3*time.Minute),
			wait.WithInterval(10*time.Second),
		); err != nil {
			log.Printf("Timed out while waiting for the controller deployment: %s", err)
			return ctx, err
		}

		return ctx, nil
	}
}

func applyTrackingPolicy() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Applying tracking policy")

		if p := utils.RunCommandContext(ctx, fmt.Sprintf("kubectl apply -f %s", trackPolicyPath)); p.Err() != nil {
			log.Printf("Failed to deploy resources: %s: %s", p.Err(), p.Out())
			return ctx, p.Err()
		}

		return ctx, nil
	}
}

func portForwarding() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Port forwarding to access api endpoints")

		cmd := exec.Command("kubectl", "port-forward", "-n", "kflashback-system", "svc/kflashback-api", "9090:9090")
		if err := cmd.Start(); err != nil {
			return ctx, fmt.Errorf("failed to start port-forward: %w", err)
		}

		time.Sleep(5 * time.Second) // wait for it to be ready

		return ctx, nil
	}
}

func cleanUpResources() env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		log.Println("Cleaning up the resources.....")

		switch storageBackend := os.Getenv("STORAGE_BACKEND"); storageBackend {
		case "sqlite":
			utils.RunCommandContext(ctx, fmt.Sprintf("kubectl delete -f %s", sqliteControllerPath))
		case "postgresql":
			utils.RunCommandContext(ctx, fmt.Sprintf("kubectl delete -f %s", postgresControllerPath))
		}
		utils.RunCommandContext(ctx, fmt.Sprintf("kubectl delete -f %s", rbacPath))
		utils.RunCommandContext(ctx, fmt.Sprintf("kubectl delete -f %s", crdPath))
		return ctx, nil
	}
}
