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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PackageReconciler reconciles a Package object
type PackageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Runner executor.Runner
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
		if d, err := time.ParseDuration(pkg.Spec.Interval); err == nil {
			pollInterval = d
		}
	}

	monitor := git.NewMonitor(repoURL)
	strat := &strategy.AlwaysDeploy{}

	commit, err := monitor.GetLatestCommit(ctx, branch)
	if err != nil {
		return ctrl.Result{RequeueAfter: pollInterval}, fmt.Errorf("failed to get latest commit: %w", err)
	}

	runner := r.Runner
	if runner == nil {
		runner = &executor.APRunner{
			Client:       r.Client,
			ImagePrefix:  os.Getenv("IMAGE_PREFIX"),
			ImageTag:     commit,
			BuildkitHost: os.Getenv("BUILDKIT_HOST"),
		}
	}

	if commit == "" {
		klog.V(4).Info("No commits found yet")
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	if pkg.Status.LastDeployedCommit == commit {
		klog.V(4).Infof("Commit %s already deployed", commit)
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	// Determine job name for this commit
	jobName := fmt.Sprintf("deploy-%s-%s", pkg.Name, commit)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	var job batchv1.Job
	jobErr := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: "autodeploy-system"}, &job)

	if jobErr != nil {
		if !errors.IsNotFound(jobErr) {
			return ctrl.Result{}, fmt.Errorf("failed to get deploy job: %w", jobErr)
		}

		// Job does not exist, trigger deployment if strategy engine decides so
		if strat.ShouldDeploy(commit, branch, nil) {
			klog.Infof("Triggering deployment for commit %s", commit)

			var args []string
			if pkg.Spec.Directory != "" {
				args = append(args, "--root="+pkg.Spec.Directory)
			}

			if err := runner.DeployFlow(ctx, &pkg, commit, args...); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to run deploy flow: %w", err)
			}

			// Update status to Deploying and requeue soon
			setCondition(&pkg, "Deployed", metav1.ConditionUnknown, "Deploying", "Deployment job is currently running")
			if err := r.Status().Update(ctx, &pkg); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status to Deploying: %w", err)
			}

			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	// Job exists, inspect status
	finished, succeeded := isJobFinished(&job)
	if !finished {
		klog.Infof("Deployment job %s is still running for commit %s", jobName, commit)
		setCondition(&pkg, "Deployed", metav1.ConditionUnknown, "Deploying", "Deployment job is currently running")
		if err := r.Status().Update(ctx, &pkg); err != nil {
			klog.V(4).Infof("Failed to update status to Deploying: %v", err)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if succeeded {
		klog.Infof("Deployment job %s succeeded for commit %s", jobName, commit)
		pkg.Status.LastDeployedCommit = commit
		setCondition(&pkg, "Deployed", metav1.ConditionTrue, "Succeeded", "Deployment job completed successfully")
		if err := r.Status().Update(ctx, &pkg); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Package status on success: %w", err)
		}
	} else {
		klog.Errorf("Deployment job %s failed for commit %s", jobName, commit)
		setCondition(&pkg, "Deployed", metav1.ConditionFalse, "Failed", "Deployment job failed")
		if err := r.Status().Update(ctx, &pkg); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Package status on failure: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func isJobFinished(job *batchv1.Job) (finished bool, succeeded bool) {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			return true, true
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return true, false
		}
	}
	if job.Status.Succeeded > 0 {
		return true, true
	}
	if job.Status.Failed > 0 {
		return true, false
	}
	return false, false
}

func setCondition(pkg *v1alpha1.Package, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	newCond := metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}

	for i, cond := range pkg.Status.Conditions {
		if cond.Type == condType {
			if cond.Status != status || cond.Reason != reason || cond.Message != message {
				pkg.Status.Conditions[i] = newCond
			}
			return
		}
	}
	pkg.Status.Conditions = append(pkg.Status.Conditions, newCond)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Package{}).
		Complete(r)
}
