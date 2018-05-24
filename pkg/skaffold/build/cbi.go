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

package build

import (
	"context"
	"io"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build/tag"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/cbi"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/kubernetes"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type CBIBuilder struct {
	*v1alpha2.BuildConfig
}

func NewCBIBuilder(cfg *v1alpha2.BuildConfig) (*CBIBuilder, error) {
	return &CBIBuilder{
		BuildConfig: cfg,
	}, nil
}

func (b *CBIBuilder) Build(ctx context.Context, out io.Writer, tagger tag.Tagger, artifacts []*v1alpha2.Artifact) (*BuildResult, error) {
	cbiT, err := b.BuildConfig.CBIBuild.GetBuildJobTemplate()
	if err != nil {
		return nil, err
	}
	clientConfig, err := kubernetes.GetClientConfig()
	if err != nil {
		return nil, err
	}
	res := &BuildResult{}

	logrus.Debugf("building %d artifacts", len(artifacts))
	// TODO(r2d4): parallel builds
	for _, artifact := range artifacts {
		initialTag, err := cbi.RunCBIBuild(ctx, out, clientConfig, artifact, cbiT)
		if err != nil {
			return nil, errors.Wrapf(err, "running cbi build for %s", artifact.ImageName)
		}
		digest, err := docker.RemoteDigest(initialTag)
		if err != nil {
			return nil, errors.Wrap(err, "getting digest")
		}

		tag, err := tagger.GenerateFullyQualifiedImageName(artifact.Workspace, &tag.TagOptions{
			ImageName: artifact.ImageName,
			Digest:    digest,
		})
		if err != nil {
			return nil, errors.Wrap(err, "generating tag")
		}

		if err := docker.AddTag(initialTag, tag); err != nil {
			return nil, errors.Wrap(err, "tagging image")
		}

		res.Builds = append(res.Builds, Build{
			ImageName: artifact.ImageName,
			Tag:       tag,
			Artifact:  artifact,
		})
	}
	return res, nil
}
