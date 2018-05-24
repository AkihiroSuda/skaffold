/*
Copyright 2018 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cbi

import (
	"context"
	_ "crypto/sha256" // for opencontainers/go-digest
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	cbiv1alpha1 "github.com/containerbuilding/cbi/pkg/apis/cbi/v1alpha1"
	cbiclientset "github.com/containerbuilding/cbi/pkg/client/clientset/versioned"
	cbiv1alpha1client "github.com/containerbuilding/cbi/pkg/client/clientset/versioned/typed/cbi/v1alpha1"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	batchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	rest "k8s.io/client-go/rest"
)

func RunCBIBuild(ctx context.Context, out io.Writer, clientConfig *rest.Config, artifact *v1alpha2.Artifact, template v1alpha2.CBIBuildJobTemplate) (string, error) {
	if v := template.APIVersion(); v != cbiv1alpha1.SchemeGroupVersion.String() {
		return "", errors.Errorf("unsupported CBI API version: %q", v)
	}
	logrus.Debugf("Running CBI Build, APIVersion=%s", template.APIVersion())
	kubeNS := "default"
	client, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return "", err
	}
	nginx := NewTempNginx(client, kubeNS)
	defer nginx.Delete(ctx)
	ctxTar, err := ioutil.TempFile("", "skaffold-cbi-temp")
	if err != nil {
		return "", err
	}
	defer os.Remove(ctxTar.Name())
	ctxTarDigester := digest.SHA256.Digester()
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		logrus.Debugf("Creating a temporary nginx server")
		return nginx.Create(egCtx)
	})
	eg.Go(func() error {
		w := io.MultiWriter(ctxTar, ctxTarDigester.Hash())
		if artifact.DockerArtifact != nil {
			dockerfilePath := artifact.DockerArtifact.DockerfilePath
			if err := docker.CreateDockerTarGzContext(w, dockerfilePath, artifact.Workspace); err != nil {
				return errors.Wrap(err, "creating tar gz")
			}
		} else {
			if err := util.CreateTarGz(w, artifact.Workspace, nil); err != nil {
				return errors.Wrap(err, "creating tar gz")
			}
		}
		return nil
	})
	if err := eg.Wait(); err != nil {
		return "", err
	}

	ctxTarDigest := ctxTarDigester.Digest()
	ctxTarDest := fmt.Sprintf("/usr/share/nginx/html/%s-%s.tar.gz", ctxTarDigest.Algorithm(), ctxTarDigest.Encoded())
	ctxTarURL := fmt.Sprintf("http://%s/%s", nginx.Service().Name, filepath.Base(ctxTarDest))

	logrus.Debugf("Uploading %s to %s/%s:%s. (%s)", ctxTar.Name(), kubeNS, nginx.Pod().Name, ctxTarDest, ctxTarURL)
	if err := nginx.Copy(ctx, ctxTarDest, ctxTar.Name()); err != nil {
		return "", err
	}
	logrus.Debugf("Upload done")
	initialTag := util.RandomID()
	imageDst := fmt.Sprintf("%s:%s", artifact.ImageName, initialTag)
	if err := template.Fulfill(imageDst, ctxTarURL); err != nil {
		return "", err
	}

	buildJob, ok := template.BuildJob().(*cbiv1alpha1.BuildJob)
	if !ok {
		return "", errors.Errorf("expected *cbiv1alpha1.BuildJob, got %+v", template.BuildJob())
	}

	logrus.Debugf("CBI BuildJob: %+v", buildJob)

	cbiC, err := cbiclientset.NewForConfig(clientConfig)
	if err != nil {
		return "", err
	}

	if err := runCBIBuildV1Alpha1(ctx, out, cbiC.CbiV1alpha1(), client, kubeNS, buildJob); err != nil {
		return "", err
	}

	return imageDst, nil
}

func runCBIBuildV1Alpha1(ctx context.Context, out io.Writer, cbiC cbiv1alpha1client.CbiV1alpha1Interface, client kubernetes.Interface, kubeNS string, bj *cbiv1alpha1.BuildJob) error {
	logrus.Debugf("creating buildjob %s", bj.Name)
	bj, err := cbiC.BuildJobs(kubeNS).Create(bj)
	if err != nil {
		return err
	}
	defer cbiC.BuildJobs(kubeNS).Delete(bj.Name, nil)

	logrus.Debugf("waiting buildjob %s to be ready", bj.Name)
	if wErr := wait.PollImmediate(time.Millisecond*500, time.Minute*3, func() (bool, error) {
		bj, err = cbiC.BuildJobs(kubeNS).Get(bj.Name, metav1.GetOptions{
			IncludeUninitialized: true,
		})
		if err != nil {
			return false, fmt.Errorf("not found: %s", bj.Name)
		}
		switch bj.Status.Job {
		case "":
			return false, nil
		default:
			return true, nil
		}
	}); wErr != nil {
		return wErr
	}

	logrus.Debugf("CBI BuildJob: %q, batchv1 Job: %q", bj.Name, bj.Status.Job)
	// the batchv1 Job will be automatically deleted (by CBI controller) on deletion of CBI BuildJob
	if err := waitJobPodReady(ctx, client.BatchV1().Jobs(kubeNS), bj.Status.Job); err != nil {
		return err
	}
	jobW := cmdFollowJobLog(ctx, out, kubeNS, bj.Status.Job)
	if err := jobW.Start(); err != nil {
		return err
	}
	defer jobW.Process.Kill()
	if err := waitJobCompletion(ctx, client.BatchV1().Jobs(kubeNS), bj.Status.Job); err != nil {
		return err
	}
	return nil
}

func waitJobPodReady(ctx context.Context, jobs batchv1.JobInterface, jobName string) error {
	// FIXME
	time.Sleep(10 * time.Second)
	return nil
}

func waitJobCompletion(ctx context.Context, jobs batchv1.JobInterface, jobName string) error {
	return wait.PollImmediate(time.Millisecond*500, time.Minute*60, func() (bool, error) {
		job, err := jobs.Get(jobName, metav1.GetOptions{
			IncludeUninitialized: true,
		})
		if err != nil {
			return false, fmt.Errorf("not found: %s", jobName)
		}
		if job.Status.CompletionTime != nil {
			if job.Status.Failed == 0 {
				return true, nil
			} else {
				return true, fmt.Errorf("job failed: %+v", job.Status)
			}
		}
		return false, nil
	})
}

func cmdFollowJobLog(ctx context.Context, w io.Writer, kubeNS, jobName string) *exec.Cmd {
	// kubectl is used so as to avoid dependency on k8s.io/kubernetes/pkg/kubectl/cmd
	// Note: the command does not exit on job completion
	cmd := exec.CommandContext(ctx, "kubectl", "--namespace", kubeNS,
		"logs", "--follow", "job/"+jobName)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd
}
