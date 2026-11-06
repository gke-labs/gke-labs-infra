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

package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/apis/infra/v1alpha1"
	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/executor"
	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/git"
	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/strategy"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// PackageReconciler reconciles a Package object
type PackageReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Runner       executor.Runner
	JobNamespace string
}

// Reconcile checks for updates and triggers deployments if necessary.
func (r *PackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("Reconciling Package %s", req.NamespacedName)

	var pkg v1alpha1.Package
	if err := r.Get(ctx, req.NamespacedName, &pkg); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	repoURL := pkg.Spec.Repo
	branch := pkg.Spec.Branch
	if branch == "" {
		branch = "main"
	}

	pollInterval := 1 * time.Minute
	if pkg.Spec.Interval != "" {
		d, err := time.ParseDuration(pkg.Spec.Interval)
		if err != nil {
			klog.Errorf("Invalid spec.interval %q for Package %s: %v", pkg.Spec.Interval, req.NamespacedName, err)
			return ctrl.Result{}, fmt.Errorf("invalid spec.interval %q: %w", pkg.Spec.Interval, err)
		}
		pollInterval = d
	}

	jobPollInterval := 10 * time.Second
	if pollInterval < jobPollInterval {
		jobPollInterval = pollInterval
	}

	monitor := git.NewMonitor(repoURL)
	strat := &strategy.AlwaysDeploy{}

	commit, err := monitor.GetLatestCommit(ctx, branch)
	if err != nil {
		return ctrl.Result{RequeueAfter: pollInterval}, fmt.Errorf("failed to get latest commit: %w", err)
	}

	if commit == "" {
		klog.V(4).Info("No commits found yet")
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	if pkg.Status.LastDeployedCommit == commit {
		klog.V(4).Infof("Commit %s already deployed", commit)
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	jobNamespace := r.JobNamespace
	if jobNamespace == "" {
		jobNamespace = executor.DefaultJobNamespace
	}
	jobName := executor.JobName(pkg.Name, commit)

	runner := r.Runner
	if runner == nil {
		runner = &executor.APRunner{
			Client:       r.Client,
			ImagePrefix:  os.Getenv("IMAGE_PREFIX"),
			ImageTag:     commit,
			BuildkitHost: os.Getenv("BUILDKIT_HOST"),
			JobNamespace: jobNamespace,
		}
	}

	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: jobNamespace, Name: jobName}, &job); err != nil {
		if errors.IsNotFound(err) {
			if strat.ShouldDeploy(commit, branch, nil) {
				klog.Infof("Triggering deployment for commit %s", commit)

				var args []string
				if pkg.Spec.Directory != "" {
					args = append(args, pkg.Spec.Directory)
				}

				if err := runner.DeployFlow(ctx, &pkg, commit, args...); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to run deploy flow: %w", err)
				}

				return ctrl.Result{RequeueAfter: jobPollInterval}, nil
			}
			return ctrl.Result{RequeueAfter: pollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get Job %s/%s: %w", jobNamespace, jobName, err)
	}

	if isJobSucceeded(&job) {
		klog.Infof("Deployment Job %s succeeded for commit %s", jobName, commit)
		pkg.Status.LastDeployedCommit = commit
		if err := r.Status().Update(ctx, &pkg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	if failed, reason := isJobFailed(&job); failed {
		klog.Errorf("Deployment Job %s failed for commit %s: %s", jobName, commit, reason)
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	klog.V(4).Infof("Deployment Job %s for commit %s is still running", jobName, commit)
	return ctrl.Result{RequeueAfter: jobPollInterval}, nil
}

func isJobSucceeded(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return job.Status.CompletionTime != nil || job.Status.Succeeded > 0
}

func isJobFailed(job *batchv1.Job) (bool, string) {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			msg := c.Reason
			if c.Message != "" {
				msg = fmt.Sprintf("%s: %s", c.Reason, c.Message)
			}
			return true, msg
		}
	}
	return false, ""
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Package{}).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findPackagesForJob),
		).
		Complete(r)
}

func (r *PackageReconciler) findPackagesForJob(ctx context.Context, obj client.Object) []reconcile.Request {
	job, ok := obj.(*batchv1.Job)
	if !ok {
		return nil
	}
	labels := job.GetLabels()
	if labels == nil {
		return nil
	}
	pkgName := labels[executor.PackageNameLabel]
	pkgNamespace := labels[executor.PackageNamespaceLabel]
	if pkgName == "" || pkgNamespace == "" {
		return nil
	}
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      pkgName,
				Namespace: pkgNamespace,
			},
		},
	}
}
