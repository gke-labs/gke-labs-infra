// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// test-runner-controller keeps a pool of ephemeral GitHub Actions runners
// running inside agent-sandbox Sandboxes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gke-labs-infra/test-runner/pkg/github"
	"github.com/gke-labs/gke-labs-infra/test-runner/pkg/runnerpool"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// Run builds and executes the root command.
func Run(ctx context.Context) error {
	var opt Options
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "test-runner-controller",
		Short: "Keeps a pool of ephemeral GitHub Actions runners running in agent-sandbox Sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return RunController(cmd.Context(), opt)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opt.Kubeconfig, "kubeconfig", opt.Kubeconfig, "Path to a kubeconfig; defaults to in-cluster config, then $KUBECONFIG / ~/.kube/config")
	f.StringVar(&opt.Namespace, "namespace", opt.Namespace, "Namespace in which runner Sandboxes are created")
	f.StringVar(&opt.Pool, "pool", opt.Pool, "Name of the runner pool; prefixes sandbox and runner names")
	f.IntVar(&opt.Replicas, "replicas", opt.Replicas, "Number of idle runners to keep waiting for jobs")
	f.StringVar(&opt.GitHubRepo, "github-repo", opt.GitHubRepo, "Register runners with this repository (owner/repo)")
	f.StringVar(&opt.GitHubOrg, "github-org", opt.GitHubOrg, "Register runners with this organization (alternative to --github-repo)")
	f.StringVar(&opt.GitHubTokenFile, "github-token-file", opt.GitHubTokenFile, "File containing the GitHub token; defaults to $GITHUB_TOKEN")
	f.StringSliceVar(&opt.RunnerLabels, "runner-labels", opt.RunnerLabels, "GitHub runner labels (defaults to the pool name); workflows select them with runs-on")
	f.Int64Var(&opt.RunnerGroupID, "runner-group-id", opt.RunnerGroupID, "GitHub runner group ID to register runners into")
	f.StringVar(&opt.RunnerImage, "runner-image", opt.RunnerImage, "GitHub Actions runner image")
	f.StringVar(&opt.RuntimeClassName, "runtime-class", opt.RuntimeClassName, "RuntimeClass for runner pods (empty for the cluster default)")
	f.DurationVar(&opt.MaxRunnerLifetime, "max-runner-lifetime", opt.MaxRunnerLifetime, "Sandboxes are torn down after this long, whatever state they are in")
	f.StringVar(&opt.CPURequest, "runner-cpu-request", opt.CPURequest, "CPU request for the runner container")
	f.StringVar(&opt.MemoryRequest, "runner-memory-request", opt.MemoryRequest, "Memory request for the runner container")
	f.StringVar(&opt.CPULimit, "runner-cpu-limit", opt.CPULimit, "CPU limit for the runner container (empty for none)")
	f.StringVar(&opt.MemoryLimit, "runner-memory-limit", opt.MemoryLimit, "Memory limit for the runner container (empty for none)")
	f.StringVar(&opt.WorkVolumeSizeLimit, "runner-work-size-limit", opt.WorkVolumeSizeLimit, "Size limit of the emptyDir backing the runner work directory")
	f.StringToStringVar(&opt.RunnerEnv, "runner-env", opt.RunnerEnv, "Extra environment variables for the runner container (key=value)")
	f.DurationVar(&opt.SyncPeriod, "sync-period", opt.SyncPeriod, "How often to reconcile even if nothing changed")
	f.DurationVar(&opt.OrphanSweepPeriod, "orphan-sweep-period", opt.OrphanSweepPeriod, "How often to remove GitHub runners that have no sandbox (0 disables)")
	f.BoolVar(&opt.PrintSandboxTemplate, "print-sandbox-template", opt.PrintSandboxTemplate, "Print the Sandbox that would be created for a runner as YAML and exit (no GitHub or cluster access needed)")

	klogFlags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(klogFlags)
	cmd.Flags().AddGoFlagSet(klogFlags)

	return cmd.ExecuteContext(ctx)
}

// Options holds the controller configuration.
type Options struct {
	Kubeconfig string
	Namespace  string
	Pool       string
	Replicas   int

	GitHubRepo      string
	GitHubOrg       string
	GitHubTokenFile string
	RunnerLabels    []string
	RunnerGroupID   int64

	RunnerImage         string
	RuntimeClassName    string
	MaxRunnerLifetime   time.Duration
	CPURequest          string
	MemoryRequest       string
	CPULimit            string
	MemoryLimit         string
	WorkVolumeSizeLimit string
	RunnerEnv           map[string]string

	SyncPeriod        time.Duration
	OrphanSweepPeriod time.Duration

	// PrintSandboxTemplate prints the Sandbox a runner would get and exits.
	PrintSandboxTemplate bool
}

// InitDefaults sets the default values for Options.
func (o *Options) InitDefaults() {
	o.Namespace = "test-runner-sandboxes"
	o.Pool = "test-runner"
	o.Replicas = 1
	o.RunnerGroupID = 1
	o.RunnerImage = "ghcr.io/actions/actions-runner:2.338.0"
	// GitHub's own hosted runners cap a job at 6 hours.
	o.MaxRunnerLifetime = 6 * time.Hour
	o.CPURequest = "1"
	o.MemoryRequest = "2Gi"
	o.MemoryLimit = "4Gi"
	o.WorkVolumeSizeLimit = "20Gi"
	o.SyncPeriod = 30 * time.Second
	o.OrphanSweepPeriod = 10 * time.Minute
}

// RunController runs the controller until ctx is cancelled.
func RunController(ctx context.Context, opt Options) error {
	if opt.PrintSandboxTemplate {
		poolOpts, err := buildPoolOptions(opt)
		if err != nil {
			return err
		}
		return runnerpool.PrintSandboxTemplate(os.Stdout, poolOpts, opt.Namespace)
	}

	scope, err := github.ParseScope(opt.GitHubRepo, opt.GitHubOrg)
	if err != nil {
		return fmt.Errorf("--github-repo/--github-org: %w", err)
	}
	token, err := loadToken(opt.GitHubTokenFile)
	if err != nil {
		return err
	}
	ghClient := github.NewClient(token, scope)

	poolOpts, err := buildPoolOptions(opt)
	if err != nil {
		return err
	}

	restConfig, err := loadRESTConfig(opt.Kubeconfig)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}
	core, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	pool, err := runnerpool.New(poolOpts, runnerpool.NewKubeClient(opt.Namespace, dyn, core), ghClient)
	if err != nil {
		return err
	}

	// Wake the pool up as soon as a sandbox changes (e.g. finishes) rather
	// than waiting for the next periodic sync.
	trigger := make(chan struct{}, 1)
	wake := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	selector := runnerpool.PoolLabel + "=" + opt.Pool
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, opt.Namespace, func(lo *metav1.ListOptions) {
		lo.LabelSelector = selector
	})
	informer := factory.ForResource(runnerpool.SandboxGVR).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { wake() },
		UpdateFunc: func(any, any) { wake() },
		DeleteFunc: func(any) { wake() },
	}); err != nil {
		return fmt.Errorf("registering sandbox informer: %w", err)
	}
	factory.Start(ctx.Done())
	defer factory.Shutdown()
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("timed out waiting for sandbox informer to sync (is the agent-sandbox CRD installed?)")
	}

	klog.Infof("managing pool %q in namespace %q for %s: %d replicas, labels %v, image %s",
		opt.Pool, opt.Namespace, scope, opt.Replicas, poolOpts.Labels, opt.RunnerImage)
	return pool.Run(ctx, trigger)
}

func buildPoolOptions(opt Options) (runnerpool.Options, error) {
	resources, err := buildResources(opt)
	if err != nil {
		return runnerpool.Options{}, err
	}
	workSize, err := parseQuantity("--runner-work-size-limit", opt.WorkVolumeSizeLimit)
	if err != nil {
		return runnerpool.Options{}, err
	}

	runnerLabels := opt.RunnerLabels
	if len(runnerLabels) == 0 {
		runnerLabels = []string{opt.Pool}
	}

	var env []corev1.EnvVar
	for _, k := range sortedKeys(opt.RunnerEnv) {
		env = append(env, corev1.EnvVar{Name: k, Value: opt.RunnerEnv[k]})
	}

	return runnerpool.Options{
		Name:          opt.Pool,
		Replicas:      opt.Replicas,
		Labels:        runnerLabels,
		RunnerGroupID: opt.RunnerGroupID,
		Runner: runnerpool.RunnerSpec{
			Image:               opt.RunnerImage,
			RuntimeClassName:    opt.RuntimeClassName,
			MaxLifetime:         opt.MaxRunnerLifetime,
			Resources:           resources,
			WorkVolumeSizeLimit: workSize,
			Env:                 env,
		},
		SyncPeriod:        opt.SyncPeriod,
		OrphanSweepPeriod: opt.OrphanSweepPeriod,
	}, nil
}

func buildResources(opt Options) (corev1.ResourceRequirements, error) {
	out := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}
	set := func(list corev1.ResourceList, name corev1.ResourceName, flag, value string) error {
		if value == "" {
			return nil
		}
		q, err := parseQuantity(flag, value)
		if err != nil {
			return err
		}
		list[name] = q
		return nil
	}
	if err := set(out.Requests, corev1.ResourceCPU, "--runner-cpu-request", opt.CPURequest); err != nil {
		return out, err
	}
	if err := set(out.Requests, corev1.ResourceMemory, "--runner-memory-request", opt.MemoryRequest); err != nil {
		return out, err
	}
	if err := set(out.Limits, corev1.ResourceCPU, "--runner-cpu-limit", opt.CPULimit); err != nil {
		return out, err
	}
	if err := set(out.Limits, corev1.ResourceMemory, "--runner-memory-limit", opt.MemoryLimit); err != nil {
		return out, err
	}
	return out, nil
}

func parseQuantity(flag, value string) (resource.Quantity, error) {
	if value == "" {
		return resource.Quantity{}, nil
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("%s: invalid quantity %q: %w", flag, value, err)
	}
	return q, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func loadToken(path string) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading --github-token-file: %w", err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return "", fmt.Errorf("--github-token-file %q is empty", path)
		}
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("a GitHub token is required: set --github-token-file or $GITHUB_TOKEN")
}

func loadRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig %q: %w", kubeconfig, err)
		}
		return cfg, nil
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubernetes config: %w", err)
	}
	return cfg, nil
}
