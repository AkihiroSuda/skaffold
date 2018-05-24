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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	kutil "github.com/GoogleContainerTools/skaffold/pkg/skaffold/kubernetes"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func NewTempNginx(clientset kubernetes.Interface, ns string) *TempNginx {
	name := fmt.Sprintf("tempnginx-%d-%s", time.Now().UnixNano(), util.RandomID()[0:2])
	port := int32(80)
	selectorKey := "nginx"
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{selectorKey: name},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "nginx",
					Image:           "nginx:alpine",
					ImagePullPolicy: v1.PullIfNotPresent,
					Ports: []v1.ContainerPort{
						{
							ContainerPort: port,
						},
					},
				},
			},
		},
	}
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Port: port,
				},
			},
			Selector: map[string]string{
				selectorKey: name,
			},
		},
	}
	return &TempNginx{
		podClient:     clientset.CoreV1().Pods(ns),
		serviceClient: clientset.CoreV1().Services(ns),
		pod:           pod,
		service:       service,
	}
}

type TempNginx struct {
	podClient     corev1.PodInterface
	serviceClient corev1.ServiceInterface
	ns            string
	pod           *v1.Pod
	service       *v1.Service
}

func (x *TempNginx) Pod() *v1.Pod {
	return x.pod
}

func (x *TempNginx) Service() *v1.Service {
	return x.service
}

// Create creates pod and service, and wait for the completion
func (x *TempNginx) Create(ctx context.Context) error {
	// TODO(AkihiroSuda): allow keeping temp nginx pool so as to reduce pod/service creation
	var err error
	x.pod, err = x.podClient.Create(x.pod)
	if err != nil {
		return err
	}
	x.service, err = x.serviceClient.Create(x.service)
	if err != nil {
		return err
	}
	if err = kutil.WaitForPodReady(x.podClient, x.pod.Name); err != nil {
		return err
	}
	return nil
}

func (x *TempNginx) Delete(ctx context.Context) error {
	if err := x.serviceClient.Delete(x.service.Name, nil); err != nil {
		return err
	}
	if err := x.podClient.Delete(x.pod.Name, nil); err != nil {
		return err
	}
	return nil
}

func (x *TempNginx) Copy(ctx context.Context, dstRemote, srcLocal string) error {
	// `kubectl cp` is used so as to avoid dependency on k8s.io/kubernetes/pkg/kubectl/cmd
	cmds := []*exec.Cmd{
		exec.CommandContext(ctx, "kubectl", "cp", srcLocal,
			fmt.Sprintf("%s/%s:%s", x.ns, x.pod.Name, dstRemote)),
		exec.CommandContext(ctx, "kubectl", "--namespace", x.ns,
			"exec", x.pod.Name, "chmod", "0644", dstRemote),
	}
	for _, cmd := range cmds {
		var b bytes.Buffer
		cmd.Stdout = io.MultiWriter(&b, os.Stderr)
		cmd.Stderr = io.MultiWriter(&b, os.Stderr)
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "output=%q", b.String())
		}
	}
	return nil
}
